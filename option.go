package dcron

import "github.com/robfig/cron/v3"

// Option is a function type for configuring dcron instance options
// It follows the functional options pattern for clean and flexible configuration
type Option func(*dcron)

// WithStrategy sets the task assignment strategy (e.g., consistent hash or average)
// This determines how tasks are distributed across nodes in the cluster
func WithStrategy(strategy AssignerStrategy) Option {
	return func(dc *dcron) {
		dc.assigner = NewAssigner(strategy)
	}
}

// WithAssigner sets a custom task assignment strategy
// This allows for advanced or non-standard assignment strategies
func WithAssigner(assigner Assigner) Option {
	return func(dc *dcron) {
		dc.assigner = assigner
	}
}

// WithCronOptions allows custom configuration of the underlying cron instance
// This passes options directly to the robfig/cron library
func WithCronOptions(options ...cron.Option) Option {
	return func(dc *dcron) {
		dc.cOptions = options
	}
}

// WithTaskRunFunc sets the handler function for dynamic task execution
// This function is called when a dynamic task is triggered
func WithTaskRunFunc(handler TaskRunFunc) Option {
	return func(dc *dcron) {
		dc.taskRunFunc = handler
	}
}

// WithErrHandler sets the error handler for task execution failures
// This allows custom error handling when tasks fail to execute
func WithErrHandler(handler ErrHandler) Option {
	return func(dc *dcron) {
		dc.errHandler = handler
	}
}

// WithLogger sets a custom logger for dcron
// If not provided, a default logger will be used
func WithLogger(log Logger) Option {
	return func(dc *dcron) {
		if log != nil {
			logger = log
		}
	}
}

// WithWeight sets the node weight for load balancing
// This option is relevant for strategies like weighted round-robin
func WithWeight(weight int) Option {
	return func(dc *dcron) {
		dc.weight = weight
	}
}

// WithIP sets the node IP for identification
func WithIP(ip string) Option {
	return func(dc *dcron) {
		dc.nodeIP = ip
	}
}
