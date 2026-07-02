package controller

import (
	"time"

	"github.com/sirupsen/logrus"

	"k8s.io/client-go/util/workqueue"
)

var (
	// maxRetries is the number of times a deployment will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a deployment is going to be requeued:
	//
	// 5ms, 10ms, 20ms
	maxRetries = 3
)

type baseController struct {
	name   string
	logger *logrus.Entry
	queue  workqueue.TypedRateLimitingInterface[any]
}

func newBaseController(name string, logger logrus.FieldLogger) *baseController {
	nameConfig := workqueue.TypedRateLimitingQueueConfig[any]{Name: name}
	return newBaseControllerWithQueue(name, logger,
		workqueue.NewTypedRateLimitingQueueWithConfig[any](EnhancedDefaultControllerRateLimiter(), nameConfig))
}

// newBaseControllerWithCappedBackoff is newBaseController with the exponential
// per-item backoff capped at maxBackoff instead of the default 1000s. Use it
// for controllers that drive the instance attach/detach ladder: parking a
// failing engine or engine frontend for up to ~16 minutes turns a transient
// storm error into a long outage for the volume it serves.
func newBaseControllerWithCappedBackoff(name string, logger logrus.FieldLogger, maxBackoff time.Duration) *baseController {
	nameConfig := workqueue.TypedRateLimitingQueueConfig[any]{Name: name}
	return newBaseControllerWithQueue(name, logger,
		workqueue.NewTypedRateLimitingQueueWithConfig[any](CappedControllerRateLimiter(maxBackoff), nameConfig))
}

func newBaseControllerWithQueue(name string, logger logrus.FieldLogger,
	queue workqueue.TypedRateLimitingInterface[any]) *baseController {
	c := &baseController{
		name:   name,
		logger: logger.WithField("controller", name),
		queue:  queue,
	}

	return c
}
