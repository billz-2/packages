package test_environment

type backgroundTask struct {
}

func NewBackgroundTask() backgroundTask {
	return backgroundTask{}
}

func (backgroundTask) Execute(task func()) {
	task()
}
