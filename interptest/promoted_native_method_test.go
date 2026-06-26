package interptest

import "testing"

// A method promoted from a NATIVE embedded field (sync.Mutex) of an interpreted
// struct, dispatched on the synth receiver, must reach the genuine native method.
// On the shared-PC (wasm) build the synth rtype's own promoted-method entry is the
// -1 trap stub, so dispatch routes to the embedded field's method (real PC) via
// vm.bindPromotedNative. Covered receiver shapes: direct concrete call, mvm-Iface
// (interpreted sync.Locker), and a native sync.Locker interface holding the synth
// pointer. These TestSynth* run on the wasm CI (native = stub-pool path).

func TestSynthPromotedNativeMutex(t *testing.T) {
	const src = `package main
import ("fmt"; "sync")
type Buf struct {
	sync.Mutex
	parts []string
}
func (b *Buf) add(s string) {            // direct call: b.Lock() on concrete *Buf
	b.Lock()
	defer b.Unlock()
	b.parts = append(b.parts, s)
}
type Counter struct {
	sync.RWMutex
	n int
}
func withLock(lk sync.Locker, fn func()) { // dispatch through sync.Locker
	lk.Lock()
	defer lk.Unlock()
	fn()
}
func main() {
	b := &Buf{}
	b.add("a"); b.add("b"); b.add("c")
	fmt.Println("buf", b.parts)

	c := &Counter{}
	withLock(c, func() { c.n += 5 })       // RWMutex promoted Lock via interface
	fmt.Println("counter", c.n)

	rl := c.RLocker()                      // sync.Locker whose Lock is RLock
	withLock(rl, func() {})
	fmt.Println("rlock ok")
}`
	want := "buf [a b c]\ncounter 5\nrlock ok\n"
	if got := evalOut(t, "promoted.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// database/sql is interpreted from the mirror on wasm (WasmDropExact bridge). Its
// driverConn embeds sync.Mutex and dispatches the promoted Lock both directly and
// through sync.Locker (withLock), exercising vm.bindPromotedNative end to end.
func TestSynthDatabaseSQL(t *testing.T) {
	const src = `package main
import ("database/sql"; "database/sql/driver"; "fmt"; "io")
type drv struct{}
func (drv) Open(string) (driver.Conn, error) { return &conn{}, nil }
type conn struct{}
func (c *conn) Prepare(q string) (driver.Stmt, error) { return &stmt{}, nil }
func (c *conn) Close() error              { return nil }
func (c *conn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("no tx") }
type stmt struct{}
func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 }
func (s *stmt) Exec(a []driver.Value) (driver.Result, error) { return driver.RowsAffected(int64(len(a) + 1)), nil }
func (s *stmt) Query(a []driver.Value) (driver.Rows, error) {
	rows := [][]driver.Value{{int64(1), "alice"}, {int64(2), "bob"}, {int64(3), "carol"}}
	var out [][]driver.Value
	for _, r := range rows {
		if len(a) == 1 {
			if lo, ok := a[0].(int64); ok && r[0].(int64) < lo {
				continue
			}
		}
		out = append(out, r)
	}
	return &rowSet{cols: []string{"id", "name"}, data: out}, nil
}
type rowSet struct {
	cols []string
	data [][]driver.Value
	pos  int
}
func (r *rowSet) Columns() []string { return r.cols }
func (r *rowSet) Close() error      { return nil }
func (r *rowSet) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}
func main() {
	sql.Register("mem", drv{})
	db, _ := sql.Open("mem", "")
	defer db.Close()
	res, _ := db.Exec("INSERT", 1, 2)
	n, _ := res.RowsAffected()
	fmt.Println("exec", n)
	rows, _ := db.Query("SELECT id, name WHERE id >= ?", 2)
	cols, _ := rows.Columns()
	fmt.Println("cols", cols)
	for rows.Next() {
		var id int
		var name string
		rows.Scan(&id, &name)
		fmt.Printf("row %d %s\n", id, name)
	}
	rows.Close()
	var name string
	db.QueryRow("SELECT id, name").Scan(new(int), &name)
	fmt.Println("first", name)
}`
	want := "exec 3\ncols [id name]\nrow 2 bob\nrow 3 carol\nfirst alice\n"
	if got := evalOut(t, "dbsql.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
