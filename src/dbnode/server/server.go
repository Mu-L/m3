	"github.com/m3db/m3/src/dbnode/x/tchannel"
	xconfig "github.com/m3db/m3x/config"
	"github.com/m3db/m3x/context"
	"github.com/m3db/m3x/ident"
	"github.com/m3db/m3x/instrument"
	xlog "github.com/m3db/m3x/log"
	"github.com/m3db/m3x/pool"
	xsync "github.com/m3db/m3x/sync"
	// Set the series cache policy
	seriesCachePolicy := cfg.Cache.SeriesConfiguration().Policy
	opts = opts.SetSeriesCachePolicy(seriesCachePolicy)

	// Apply pooling options
	opts = withEncodingAndPoolingOptions(cfg, logger, opts, cfg.PoolingPolicy)

		})
	resultsPool := index.NewResultsPool(
		poolOptions(policy.IndexResultsPool, scope.SubScope("index-results-pool")))
		SetResultsPool(resultsPool)
	resultsPool.Init(func() index.Results {
		return index.NewResults(nil, index.ResultsOptions{}, indexOpts)