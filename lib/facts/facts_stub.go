//go:build !linux

package facts

import "sync"

// Non-Linux platforms are not supported: these stubs only keep the package buildable.

func (f *SystemFacts) getSysInfo(wg *sync.WaitGroup)   { wg.Done() }
func (f *SystemFacts) getOSRelease(wg *sync.WaitGroup) { wg.Done() }
func (f *SystemFacts) getUname(wg *sync.WaitGroup)     { wg.Done() }
