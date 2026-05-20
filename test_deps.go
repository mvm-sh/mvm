package main

import (
	"io"
	"reflect"
	"regexp"
	"time"
)

// statDeps implements the unexported testing.testDeps interface that
// testing.MainStart requires (see ADR-019 for why mvm test drives MainStart).
// An external type can satisfy that unexported interface because its fuzzing
// methods are typed over corpusEntry, which testing declares as an alias to an
// anonymous struct -- corpusEntry below spells the same one. If a future Go
// revises the testDeps method set, MainStart(statDeps{}, ...) stops compiling,
// a build-time signal to update these stubs. Only MatchString does real work
// (powering -run/-bench/-skip); the rest are no-ops for profiling, coverage,
// testlog, and fuzzing, which mvm test does not drive.
type statDeps struct{}

type corpusEntry = struct {
	Parent     string
	Path       string
	Data       []byte
	Values     []any
	Generation int
	IsSeed     bool
}

func (statDeps) MatchString(pat, str string) (bool, error) { return regexp.MatchString(pat, str) }

func (statDeps) ImportPath() string                          { return "" }
func (statDeps) ModulePath() string                          { return "" }
func (statDeps) SetPanicOnExit0(bool)                        {}
func (statDeps) StartCPUProfile(io.Writer) error             { return nil }
func (statDeps) StopCPUProfile()                             {}
func (statDeps) StartTestLog(io.Writer)                      {}
func (statDeps) StopTestLog() error                          { return nil }
func (statDeps) WriteProfileTo(string, io.Writer, int) error { return nil }
func (statDeps) ResetCoverage()                              {}
func (statDeps) SnapshotCoverage()                           {}

func (statDeps) CoordinateFuzzing(time.Duration, int64, time.Duration, int64, int, []corpusEntry, []reflect.Type, string, string) error {
	return nil
}
func (statDeps) RunFuzzWorker(func(corpusEntry) error) error              { return nil }
func (statDeps) ReadCorpus(string, []reflect.Type) ([]corpusEntry, error) { return nil, nil }
func (statDeps) CheckCorpus([]any, []reflect.Type) error                  { return nil }

func (statDeps) InitRuntimeCoverage() (mode string, tearDown func(string, string) (string, error), snapcov func() float64) {
	return "", nil, nil
}
