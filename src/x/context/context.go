// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package context

import (
	stdctx "context"
	"fmt"
	"sync"

	lightstep "github.com/lightstep/lightstep-tracer-go"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/mocktracer"
	"github.com/uber/jaeger-client-go"

	xopentracing "github.com/m3db/m3/src/x/opentracing"
	xresource "github.com/m3db/m3/src/x/resource"
)

const (
	maxDistanceFromRootContext = 100
)

var (
	noopTracer opentracing.NoopTracer

	errSpanTooDeep = fmt.Errorf("span created exceeds maximum depth allowed (%d)", maxDistanceFromRootContext)
)

// NB(r): using golang.org/x/net/context is too GC expensive.
// Instead, we just embed one.
type ctx struct {
	sync.RWMutex

	goCtx                stdctx.Context
	pool                 contextPool
	done                 bool
	wg                   sync.WaitGroup
	finalizeables        *finalizeableList
	parent               Context
	distanceFromRoot     uint16
	checkedAndNotSampled bool
}

type finalizeable struct {
	finalizer xresource.Finalizer
	closer    xresource.SimpleCloser
}

// NewWithGoContext creates a new context with the provided go ctx.
func NewWithGoContext(goCtx stdctx.Context) Context {
	ctx := newContext()
	ctx.SetGoContext(goCtx)
	return ctx
}

// NewBackground creates a new context with a Background go ctx.
func NewBackground() Context {
	return NewWithGoContext(stdctx.Background())
}

// NewPooledContext returns a new context that is returned to a pool when closed.
func newPooledContext(pool contextPool) Context {
	return &ctx{pool: pool}
}

// newContext returns an empty ctx
func newContext() *ctx {
	return &ctx{}
}

func (c *ctx) GoContext() stdctx.Context {
	return c.goCtx
}

func (c *ctx) SetGoContext(v stdctx.Context) {
	c.goCtx = v
}

func (c *ctx) IsClosed() bool {
	parent := c.parentCtx()
	if parent != nil {
		return parent.IsClosed()
	}

	c.RLock()
	done := c.done
	c.RUnlock()

	return done
}

func (c *ctx) RegisterFinalizer(f xresource.Finalizer) {
	parent := c.parentCtx()
	if parent != nil {
		parent.RegisterFinalizer(f)
		return
	}

	c.registerFinalizeable(finalizeable{finalizer: f})
}

func (c *ctx) RegisterCloser(f xresource.SimpleCloser) {
	parent := c.parentCtx()
	if parent != nil {
		parent.RegisterCloser(f)
		return
	}

	c.registerFinalizeable(finalizeable{closer: f})
}

func (c *ctx) registerFinalizeable(f finalizeable) {
	if c.Lock(); c.done {
		c.Unlock()
		return
	}

	if c.finalizeables == nil {
		if c.pool != nil {
			c.finalizeables = c.pool.getFinalizeablesList()
		} else {
			c.finalizeables = newFinalizeableList(nil)
		}
	}
	c.finalizeables.PushBack(f)

	c.Unlock()
}

func (c *ctx) numFinalizeables() int {
	if c.finalizeables == nil {
		return 0
	}
	return c.finalizeables.Len()
}

func (c *ctx) DependsOn(blocker Context) {
	parent := c.parentCtx()
	if parent != nil {
		parent.DependsOn(blocker)
		return
	}

	c.Lock()

	if !c.done {
		c.wg.Add(1)
		blocker.RegisterFinalizer(c)
	}

	c.Unlock()
}

// Finalize handles a call from another context that was depended upon closing.
func (c *ctx) Finalize() {
	c.wg.Done()
}

type closeMode int

const (
	closeAsync closeMode = iota
	closeBlock
)

type returnToPoolMode int

const (
	returnToPool returnToPoolMode = iota
	reuse
)

func (c *ctx) Close() {
	returnMode := returnToPool
	parent := c.parentCtx()
	if parent != nil {
		if !parent.IsClosed() {
			parent.Close()
		}
		c.tryReturnToPool(returnMode)
		return
	}

	c.close(closeAsync, returnMode)
}

func (c *ctx) BlockingClose() {
	returnMode := returnToPool
	parent := c.parentCtx()
	if parent != nil {
		if !parent.IsClosed() {
			parent.BlockingClose()
		}
		c.tryReturnToPool(returnMode)
		return
	}

	c.close(closeBlock, returnMode)
}

func (c *ctx) BlockingCloseReset() {
	returnMode := reuse
	parent := c.parentCtx()
	if parent != nil {
		if !parent.IsClosed() {
			parent.BlockingCloseReset()
		}
		c.tryReturnToPool(returnMode)
		return
	}

	c.close(closeBlock, returnMode)
	c.Reset()
}

func (c *ctx) close(mode closeMode, returnMode returnToPoolMode) {
	if c.Lock(); c.done {
		c.Unlock()
		return
	}

	c.done = true

	// Capture finalizeables to avoid concurrent r/w if Reset
	// is used after a caller waits for the finalizers to finish
	f := c.finalizeables
	c.finalizeables = nil

	c.Unlock()

	if f == nil {
		c.tryReturnToPool(returnMode)
		return
	}

	switch mode {
	case closeAsync:
		go c.finalize(f, returnMode)
	case closeBlock:
		c.finalize(f, returnMode)
	}
}

func (c *ctx) finalize(f *finalizeableList, returnMode returnToPoolMode) {
	// Wait for dependencies.
	c.wg.Wait()

	// Now call finalizers.
	for elem := f.Front(); elem != nil; elem = elem.Next() {
		if elem.Value.finalizer != nil {
			elem.Value.finalizer.Finalize()
		}
		if elem.Value.closer != nil {
			elem.Value.closer.Close()
		}
	}

	if c.pool != nil {
		// NB(r): Always return finalizeables, only the
		// context itself might want to be reused immediately.
		c.pool.putFinalizeablesList(f)
	}

	c.tryReturnToPool(returnMode)
}

func (c *ctx) Reset() {
	parent := c.parentCtx()
	if parent != nil {
		parent.Reset()
		return
	}

	c.Lock()
	c.done, c.finalizeables, c.goCtx, c.checkedAndNotSampled = false, nil, stdctx.Background(), false
	c.distanceFromRoot = 0
	c.Unlock()
}

func (c *ctx) tryReturnToPool(returnMode returnToPoolMode) {
	if c.pool == nil || returnMode != returnToPool {
		return
	}

	c.Reset()
	c.pool.Put(c)
}

func (c *ctx) newChildContext() Context {
	var childCtx *ctx
	if c.pool != nil {
		pooled, ok := c.pool.Get().(*ctx)
		if ok {
			childCtx = pooled
		}
	}

	if childCtx == nil {
		childCtx = newContext()
	}

	childCtx.setParentCtx(c)
	return childCtx
}

func (c *ctx) setParentCtx(parentCtx Context) {
	c.Lock()
	c.parent = parentCtx
	c.distanceFromRoot = parentCtx.DistanceFromRootContext() + 1
	c.Unlock()
}

func (c *ctx) parentCtx() Context {
	c.RLock()
	parent := c.parent
	c.RUnlock()

	return parent
}

func (c *ctx) DistanceFromRootContext() uint16 {
	c.RLock()
	distanceFromRootContext := c.distanceFromRoot
	c.RUnlock()

	return distanceFromRootContext
}

func (c *ctx) CheckedAndNotSampled() bool {
	c.RLock()
	checkedAndNotSampled := c.checkedAndNotSampled
	c.RUnlock()

	return checkedAndNotSampled
}

func (c *ctx) setCheckedAndNotSampled(b bool) {
	c.Lock()
	c.checkedAndNotSampled = b
	c.Unlock()
}

func (c *ctx) StartSampledTraceSpan(name string) (Context, opentracing.Span, bool) {
	if c.CheckedAndNotSampled() || c.DistanceFromRootContext() >= maxDistanceFromRootContext {
		return c, noopTracer.StartSpan(name), false
	}
	goCtx := c.GoContext()

	childGoCtx, span, sampled := StartSampledTraceSpan(goCtx, name)
	if !sampled {
		c.setCheckedAndNotSampled(true)
		return c, noopTracer.StartSpan(name), false
	}

	child := c.newChildContext()
	child.SetGoContext(childGoCtx)
	if child.DistanceFromRootContext() == maxDistanceFromRootContext {
		ext.LogError(span, errSpanTooDeep)
	}
	return child, span, true
}

func (c *ctx) StartTraceSpan(name string) (Context, opentracing.Span) {
	ctx, sp, _ := c.StartSampledTraceSpan(name)
	return ctx, sp
}

// StartSampledTraceSpan starts a span that may or may not be sampled and will
// return whether it was sampled or not.
func StartSampledTraceSpan(ctx stdctx.Context, name string, opts ...opentracing.StartSpanOption) (stdctx.Context, opentracing.Span, bool) {
	sp, spCtx := xopentracing.StartSpanFromContext(ctx, name, opts...)
	sampled := spanIsSampled(sp)
	if !sampled {
		return ctx, noopTracer.StartSpan(name), false
	}
	return spCtx, sp, true
}

func spanIsSampled(sp opentracing.Span) bool {
	if sp == nil {
		return false
	}

	// Until OpenTracing supports the `IsSampled()` method, we need to cast to a Jaeger/Lightstep/etc. spans.
	// See https://github.com/opentracing/specification/issues/92 for more information.
	spanCtx := sp.Context()
	jaegerSpCtx, ok := spanCtx.(jaeger.SpanContext)
	if ok && jaegerSpCtx.IsSampled() {
		return true
	}

	lightstepSpCtx, ok := spanCtx.(lightstep.SpanContext)
	if ok && lightstepSpCtx.TraceID != 0 {
		return true
	}

	mockSpCtx, ok := spanCtx.(mocktracer.MockSpanContext)
	if ok && mockSpCtx.Sampled {
		return true
	}

	return false
}
