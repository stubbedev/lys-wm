package wire

import "golang.org/x/sys/unix"

type fdQueue struct {
	buf  []int
	head int
	n    int
}

func newFDQueue(capacity int) fdQueue {
	return fdQueue{buf: make([]int, capacity)}
}

func (q *fdQueue) push(fd int) bool {
	if q.n == len(q.buf) {
		return false
	}
	q.buf[(q.head+q.n)%len(q.buf)] = fd
	q.n++
	return true
}

func (q *fdQueue) peek(i int) (int, bool) {
	if i >= q.n {
		return 0, false
	}
	return q.buf[(q.head+i)%len(q.buf)], true
}

func (q *fdQueue) drop(k int) {
	q.head = (q.head + k) % len(q.buf)
	q.n -= k
}

func (q *fdQueue) closeFirst(k int) {
	for i := range k {
		fd, _ := q.peek(i)
		_ = unix.Close(fd)
	}
	q.drop(k)
}

func (q *fdQueue) list() []int {
	return q.first(q.n)
}

func (q *fdQueue) first(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = q.buf[(q.head+i)%len(q.buf)]
	}
	return out
}
