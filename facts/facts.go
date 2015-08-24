// This file contains the Facts structure and functions to help build facts.
package facts

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jtopjian/terminus/config"
	"github.com/jtopjian/terminus/utils"
)

type Facts struct {
	Facts map[string]interface{}
	mu    sync.Mutex
}

func New() *Facts {
	m := make(map[string]interface{})
	return &Facts{Facts: m}
}

func (f *Facts) Add(key string, value interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Facts[key] = value
}

func ProcessExternalFacts(c config.Config, f *Facts) {
	d, err := os.Open(c.ExternalFactsDir)
	if err != nil {
		utils.Error.Println(err)
		return
	}
	defer d.Close()

	files, err := d.Readdir(0)
	if err != nil {
		utils.Error.Println(err)
		return
	}

	executableFacts := make([]string, 0)
	staticFacts := make([]string, 0)

	for _, fi := range files {
		fact := strings.TrimSuffix(fi.Name(), ".json")
		if c.Path == "" || (c.Path != "" && strings.Contains(c.Path, fact)) {
			name := filepath.Join(c.ExternalFactsDir, fi.Name())
			if isExecutable(fi) {
				executableFacts = append(executableFacts, name)
				continue
			}
			if strings.HasSuffix(name, ".json") {
				staticFacts = append(staticFacts, name)
			}
		}
	}

	var wg sync.WaitGroup
	for _, p := range staticFacts {
		p := p
		wg.Add(1)
		go factsFromFile(p, f, &wg)
	}
	for _, p := range executableFacts {
		p := p
		wg.Add(1)
		go factsFromExec(p, f, &wg)
	}
	wg.Wait()
}

func CharsToString(ca [65]int8) string {
	s := make([]byte, len(ca))
	var lens int
	for ; lens < len(ca); lens++ {
		if ca[lens] == 0 {
			break
		}
		s[lens] = uint8(ca[lens])
	}
	return string(s[0:lens])
}

func ReadFileAndReturnValue(fileName string) (string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := ioutil.ReadAll(file)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

func factsFromFile(path string, f *Facts, wg *sync.WaitGroup) {
	defer wg.Done()
	data, err := ioutil.ReadFile(path)
	if err != nil {
		utils.Error.Println(err)
		return
	}
	var result interface{}
	err = json.Unmarshal(data, &result)
	if err != nil {
		utils.Error.Println(err)
		return
	}
	f.Add(strings.TrimSuffix(filepath.Base(path), ".json"), result)
}

func factsFromExec(path string, f *Facts, wg *sync.WaitGroup) {
	defer wg.Done()
	out, err := exec.Command(path).Output()
	if err != nil {
		utils.Error.Println(err)
		return
	}
	var result interface{}
	err = json.Unmarshal(out, &result)
	if err != nil {
		utils.Error.Println(err)
		return
	}
	f.Add(filepath.Base(path), result)
}

func isExecutable(fi os.FileInfo) bool {
	if m := fi.Mode(); !m.IsDir() && m&0111 != 0 {
		return true
	}
	return false
}
