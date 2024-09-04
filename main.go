package main

import "github.com/billz-2/packages/pkg/bug_notifier"

func main() {
	_ = bug_notifier.NewBugsnag(bug_notifier.Config{})
}
