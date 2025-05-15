package queue

import (
	"time"

	"k8s.io/client-go/util/workqueue"
)

type delayingQueue struct {
	workqueue.DelayingInterface
}

func NewDelayingQueue() DelayingInterface {
	return &delayingQueue{
		workqueue.NewDelayingQueue(), // 使用标准实现
	}
}

// 确保实现所有方法
func (q *delayingQueue) ShutDown() {
	q.DelayingInterface.ShutDown()
}

func (q *delayingQueue) AddAfter(item interface{}, duration time.Duration) {
	q.DelayingInterface.AddAfter(item, duration)
}

// 如果不需要特殊处理 Forget，直接代理原始实现
func (q *delayingQueue) Forget(item interface{}) {
	if rateLimiting, ok := q.DelayingInterface.(workqueue.RateLimitingInterface); ok {
		rateLimiting.Forget(item)
	}
}
