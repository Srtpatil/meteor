package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/odpf/meteor/models"
	"github.com/odpf/meteor/plugins"
	"github.com/odpf/meteor/recipe"
	"github.com/odpf/meteor/registry"
	"github.com/odpf/salt/log"
	"github.com/pkg/errors"
)

const defaultBatchSize = 1

// TimerFn of function type
type TimerFn func() func() int

// Agent runs recipes for specified plugins.
type Agent struct {
	extractorFactory *registry.ExtractorFactory
	processorFactory *registry.ProcessorFactory
	sinkFactory      *registry.SinkFactory
	monitor          Monitor
	logger           log.Logger
	retrier          *retrier
	stopOnSinkError  bool
	timerFn          TimerFn
}

// NewAgent returns an Agent with plugin factories.
func NewAgent(config Config) *Agent {
	mt := config.Monitor
	if isNilMonitor(mt) {
		mt = new(defaultMonitor)
	}

	timerFn := config.TimerFn
	if timerFn == nil {
		timerFn = startDuration
	}

	retrier := newRetrier(config.MaxRetries, config.RetryInitialInterval)
	return &Agent{
		extractorFactory: config.ExtractorFactory,
		processorFactory: config.ProcessorFactory,
		sinkFactory:      config.SinkFactory,
		stopOnSinkError:  config.StopOnSinkError,
		monitor:          mt,
		logger:           config.Logger,
		retrier:          retrier,
		timerFn:          timerFn,
	}
}

// Validate checks the recipe for linting errors.
func (r *Agent) Validate(rcp recipe.Recipe) (errs []error) {
	if ext, err := r.extractorFactory.Get(rcp.Source.Type); err != nil {
		errs = append(errs, errors.Wrapf(err, "invalid config for %s (%s)", rcp.Source.Type, plugins.PluginTypeExtractor))
	} else {
		if err = ext.Validate(rcp.Source.Config); err != nil {
			errs = append(errs, errors.Wrapf(err, "invalid config for %s (%s)", rcp.Source.Type, plugins.PluginTypeExtractor))
		}
	}

	for _, s := range rcp.Sinks {
		sink, err := r.sinkFactory.Get(s.Name)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "invalid config for %s (%s)", rcp.Source.Type, plugins.PluginTypeExtractor))
			continue
		}
		if err = sink.Validate(s.Config); err != nil {
			errs = append(errs, errors.Wrapf(err, "invalid config for %s (%s)", s.Name, plugins.PluginTypeSink))
		}
	}

	for _, p := range rcp.Processors {
		procc, err := r.processorFactory.Get(p.Name)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "invalid config for %s (%s)", rcp.Source.Type, plugins.PluginTypeExtractor))
			continue
		}
		if err = procc.Validate(p.Config); err != nil {
			errs = append(errs, errors.Wrapf(err, "invalid config for %s (%s)", p.Name, plugins.PluginTypeProcessor))
		}
	}
	return
}

// RunMultiple executes multiple recipes.
func (r *Agent) RunMultiple(recipes []recipe.Recipe) []Run {
	var wg sync.WaitGroup
	runs := make([]Run, len(recipes))

	for i, recipe := range recipes {
		wg.Add(1)

		tempIndex := i
		tempRecipe := recipe
		go func() {
			run := r.Run(tempRecipe)
			runs[tempIndex] = run
			wg.Done()
		}()
	}

	wg.Wait()

	return runs
}

// Run executes the specified recipe.
func (r *Agent) Run(recipe recipe.Recipe) (run Run) {
	run.Recipe = recipe
	r.logger.Info("running recipe", "recipe", run.Recipe.Name)

	var (
		ctx         = context.Background()
		getDuration = r.timerFn()
		stream      = newStream()
		recordCount = 0
	)

	defer func() {
		durationInMs := getDuration()
		r.logAndRecordMetrics(run, durationInMs)
	}()

	runExtractor, err := r.setupExtractor(ctx, recipe.Source, stream)
	if err != nil {
		run.Error = errors.Wrap(err, "failed to setup extractor")
		return
	}

	for _, pr := range recipe.Processors {
		if err := r.setupProcessor(ctx, pr, stream); err != nil {
			run.Error = errors.Wrap(err, "failed to setup processor")
			return
		}
	}

	for _, sr := range recipe.Sinks {
		if err := r.setupSink(ctx, sr, stream); err != nil {
			run.Error = errors.Wrap(err, "failed to setup sink")
			return
		}
	}

	// to gather total number of records extracted
	stream.setMiddleware(func(src models.Record) (models.Record, error) {
		recordCount++
		return src, nil
	})

	// create a goroutine to let extractor concurrently emit data
	// while stream is listening via stream.Listen().
	go func() {
		defer func() {
			if r := recover(); r != nil {
				run.Error = fmt.Errorf("%s", r)
			}
			stream.Close()
		}()
		err = runExtractor()
		if err != nil {
			run.Error = errors.Wrap(err, "failed to run extractor")
		}
	}()

	// start listening.
	// this process is blocking
	if err := stream.broadcast(); err != nil {
		run.Error = errors.Wrap(err, "failed to broadcast stream")
	}

	// code will reach here stream.Listen() is done.
	run.RecordCount = recordCount
	success := run.Error == nil
	run.Success = success
	return
}

func (r *Agent) setupExtractor(ctx context.Context, sr recipe.SourceRecipe, str *stream) (runFn func() error, err error) {
	extractor, err := r.extractorFactory.Get(sr.Type)
	if err != nil {
		err = errors.Wrapf(err, "could not find extractor \"%s\"", sr.Type)
		return
	}
	if err = extractor.Init(ctx, sr.Config); err != nil {
		err = errors.Wrapf(err, "could not initiate extractor \"%s\"", sr.Type)
		return
	}

	runFn = func() (err error) {
		if err = extractor.Extract(ctx, str.push); err != nil {
			err = errors.Wrapf(err, "error running extractor \"%s\"", sr.Type)
		}

		return
	}
	return
}

func (r *Agent) setupProcessor(ctx context.Context, pr recipe.ProcessorRecipe, str *stream) (err error) {
	var proc plugins.Processor
	if proc, err = r.processorFactory.Get(pr.Name); err != nil {
		return errors.Wrapf(err, "could not find processor \"%s\"", pr.Name)
	}
	if err = proc.Init(ctx, pr.Config); err != nil {
		return errors.Wrapf(err, "could not initiate processor \"%s\"", pr.Name)
	}

	str.setMiddleware(func(src models.Record) (dst models.Record, err error) {
		dst, err = proc.Process(ctx, src)
		if err != nil {
			err = errors.Wrapf(err, "error running processor \"%s\"", pr.Name)
			return
		}

		return
	})

	return
}

func (r *Agent) setupSink(ctx context.Context, sr recipe.SinkRecipe, stream *stream) (err error) {
	var sink plugins.Syncer
	if sink, err = r.sinkFactory.Get(sr.Name); err != nil {
		return errors.Wrapf(err, "could not find sink \"%s\"", sr.Name)
	}
	if err = sink.Init(ctx, sr.Config); err != nil {
		return errors.Wrapf(err, "could not initiate sink \"%s\"", sr.Name)
	}

	retryNotification := func(e error, d time.Duration) {
		r.logger.Info(
			fmt.Sprintf("retrying sink in %d", d),
			"sink", sr.Name,
			"error", e.Error())
	}
	stream.subscribe(func(records []models.Record) error {
		err := r.retrier.retry(func() error {
			err := sink.Sink(ctx, records)
			return err
		}, retryNotification)

		// error (after exhausted retries) will just be skipped and logged
		if err != nil {
			r.logger.Error("error running sink", "sink", sr.Name, "error", err.Error())
			if !r.stopOnSinkError {
				err = nil
			}
		}

		// TODO: create a new error to signal stopping stream.
		// returning nil so stream wont stop.
		return err
	}, defaultBatchSize)

	stream.onClose(func() {
		if err = sink.Close(); err != nil {
			r.logger.Warn("error closing sink", "sink", sr.Name, "error", err)
		}
	})

	return
}

// startDuration starts a timer.
func startDuration() func() int {
	start := time.Now()
	return func() int {
		duration := time.Since(start).Milliseconds()
		return int(duration)
	}
}

func (r *Agent) logAndRecordMetrics(run Run, durationInMs int) {
	run.DurationInMs = durationInMs
	r.monitor.RecordRun(run)
	if run.Success {
		r.logger.Info("done running recipe", "recipe", run.Recipe.Name, "duration_ms", durationInMs, "record_count", run.RecordCount)
	} else {
		r.logger.Error("error running recipe", "recipe", run.Recipe.Name, "duration_ms", durationInMs, "records_count", run.RecordCount, "err", run.Error)
	}
}
