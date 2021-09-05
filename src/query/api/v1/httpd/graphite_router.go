package httpd

import (
	"net/http"

	"github.com/m3db/m3/src/query/api/v1/options"
)

type renderRouter struct {
	renderHandler func(http.ResponseWriter, *http.Request)
}

func NewGraphiteRenderRouter() options.GraphiteRenderRouter {
	return &renderRouter{}
}

func (r *renderRouter) Setup(opts options.GraphiteRenderRouterOptions) {
	r.renderHandler = opts.RenderHandler
}

func (r *renderRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.renderHandler(w, req)
}

type findRouter struct {
	findHandler func(http.ResponseWriter, *http.Request)
}

func NewGraphiteFindRouter() options.GraphiteFindRouter {
	return &findRouter{}
}

func (r *findRouter) Setup(opts options.GraphiteFindRouterOptions) {
	r.findHandler = opts.FindHandler
}

func (r *findRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.findHandler(w, req)
}
