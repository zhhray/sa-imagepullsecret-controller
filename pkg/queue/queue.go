package queue

import "k8s.io/client-go/util/workqueue"

type DelayingInterface interface {
	workqueue.DelayingInterface // 直接继承原始接口
}
