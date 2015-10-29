// +build windows

package facts

import (
	"log"
	"sync"
)

func (f *SystemFacts) getSysInfo(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("getSysInfo NOT yet implemented")
	return
}

func (f *SystemFacts) getOSRelease(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("getOSRelease NOT yet implemented")
	return
}

func (f *SystemFacts) getUname(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("getUname NOT yet implemented")
	return
}
