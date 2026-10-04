//go:build linux && (amd64 || arm64)

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

const DB_SIG = "ReldbGoStorage01"

var errCorrupt = errors.New("database file is corrupt")

type KV struct {
	Path string

	NoSync bool

	open   bool
	fd     int
	tree   BTree
	failed bool

	inTx   bool
	txMeta []byte

	free   []uint64
	pinned []uint64
	spN    int

	mmap struct {
		total  int
		chunks [][]byte
	}
	page struct {
		flushed uint64
		temp    [][]byte
	}
}

func (db *KV) Open() error {
	fd, err := createFileSync(db.Path)
	if err != nil {
		return fmt.Errorf("KV open: %w", err)
	}

	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			_ = syscall.Close(fd)
			return fmt.Errorf("database %s is in use by another process", db.Path)
		}

	}
	db.fd = fd
	db.open = true

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = db.Close()
		return fmt.Errorf("fstat: %w", err)
	}
	size := st.Size
	if size > 0 {
		if err := extendMmap(db, int(size)); err != nil {
			_ = db.Close()
			return err
		}
	}
	db.tree.get = db.pageRead
	db.tree.new = db.pageAppend
	db.tree.del = db.pageDel
	if err := readRoot(db, size); err != nil {
		_ = db.Close()
		return err
	}
	return nil
}

func (db *KV) Close() error {
	if !db.open {
		return nil
	}
	for _, chunk := range db.mmap.chunks {
		_ = syscall.Munmap(chunk)
	}
	db.mmap.chunks, db.mmap.total = nil, 0
	db.open = false
	return syscall.Close(db.fd)
}

func createFileSync(file string) (int, error) {
	dirfd, err := syscall.Open(filepath.Dir(file), syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return -1, fmt.Errorf("open directory: %w", err)
	}
	defer syscall.Close(dirfd)

	fd, err := syscall.Openat(dirfd, filepath.Base(file), syscall.O_RDWR|syscall.O_CREAT, 0o644)
	if err != nil {
		return -1, fmt.Errorf("open file: %w", err)
	}
	if err := syscall.Fsync(dirfd); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("fsync directory: %w", err)
	}
	return fd, nil
}

func saveMeta(db *KV) []byte {
	var data [32]byte
	copy(data[:16], DB_SIG)
	binary.LittleEndian.PutUint64(data[16:], db.tree.root)
	binary.LittleEndian.PutUint64(data[24:], db.page.flushed)
	return data[:]
}

func loadMeta(db *KV, data []byte) {
	db.tree.root = binary.LittleEndian.Uint64(data[16:])
	db.page.flushed = binary.LittleEndian.Uint64(data[24:])
}

func readRoot(db *KV, size int64) error {
	if size == 0 {
		db.page.flushed = 1
		return nil
	}
	if size < 32 {
		return fmt.Errorf("%w: file too small", errCorrupt)
	}
	meta := db.mmap.chunks[0][:32]
	sig := string(meta[:16])
	if sig == string(make([]byte, 16)) {

		db.tree.root, db.page.flushed = 0, 1
		return nil
	}
	if sig != DB_SIG {
		return fmt.Errorf("%w: bad signature (not a reldb file?)", errCorrupt)
	}
	loadMeta(db, meta)
	if db.page.flushed == 0 || int64(db.page.flushed)*BTREE_PAGE_SIZE > size {
		return fmt.Errorf("%w: file is truncated", errCorrupt)
	}
	if db.tree.root != 0 && db.tree.root >= db.page.flushed {
		return fmt.Errorf("%w: root page out of range", errCorrupt)
	}
	return nil
}

func extendMmap(db *KV, size int) error {
	if size <= db.mmap.total {
		return nil
	}
	alloc := max(db.mmap.total, 64<<20)
	for db.mmap.total+alloc < size {
		alloc *= 2
	}
	chunk, err := syscall.Mmap(db.fd, int64(db.mmap.total), alloc,
		syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap: %w", err)
	}
	db.mmap.total += alloc
	db.mmap.chunks = append(db.mmap.chunks, chunk)
	return nil
}

func (db *KV) pageRead(ptr uint64) []byte {
	if ptr >= db.page.flushed {
		return db.page.temp[ptr-db.page.flushed]
	}
	start := uint64(0)
	for _, chunk := range db.mmap.chunks {
		end := start + uint64(len(chunk))/BTREE_PAGE_SIZE
		if ptr < end {
			offset := BTREE_PAGE_SIZE * (ptr - start)
			return chunk[offset : offset+BTREE_PAGE_SIZE]
		}
		start = end
	}
	panic("bad page pointer")
}

func (db *KV) pageAppend(node []byte) uint64 {
	assert(len(node) == BTREE_PAGE_SIZE)
	if n := len(db.free); n > 0 {
		ptr := db.free[n-1]
		db.free = db.free[:n-1]
		db.page.temp[ptr-db.page.flushed] = node
		return ptr
	}
	ptr := db.page.flushed + uint64(len(db.page.temp))
	db.page.temp = append(db.page.temp, node)
	return ptr
}

func (db *KV) pageDel(ptr uint64) {
	if ptr < db.page.flushed {
		return
	}
	if int(ptr-db.page.flushed) < db.spN {
		db.pinned = append(db.pinned, ptr)
	} else {
		db.free = append(db.free, ptr)
	}
}

func (db *KV) dropTemp() {
	db.page.temp = db.page.temp[:0]
	db.free, db.pinned, db.spN = db.free[:0], db.pinned[:0], 0
}

func pwritev(fd int, bufs [][]byte, off int64) error {
	const maxIov = 1024 // IOV_MAX on Linux
	for len(bufs) > 0 {
		n := min(len(bufs), maxIov)
		iov := make([]syscall.Iovec, n)
		total := 0
		for i := 0; i < n; i++ {
			iov[i].Base = &bufs[i][0]
			iov[i].SetLen(len(bufs[i]))
			total += len(bufs[i])
		}
		written, _, errno := syscall.Syscall6(syscall.SYS_PWRITEV,
			uintptr(fd), uintptr(unsafe.Pointer(&iov[0])), uintptr(n), uintptr(off), 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		if int(written) != total {
			done, pos := int(written), off
			for _, b := range bufs[:n] {
				if done >= len(b) {
					done -= len(b)
				} else if err := pwriteFull(fd, b[done:], pos+int64(done)); err != nil {
					return err
				} else {
					done = 0
				}
				pos += int64(len(b))
			}
		}
		off += int64(total)
		bufs = bufs[n:]
	}
	return nil
}

func pwriteFull(fd int, p []byte, off int64) error {
	for len(p) > 0 {
		n, err := syscall.Pwrite(fd, p, off)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		p, off = p[n:], off+int64(n)
	}
	return nil
}

func (db *KV) sync() error {
	if db.NoSync {
		return nil
	}
	return syscall.Fsync(db.fd)
}

func writePages(db *KV) error {
	size := (int(db.page.flushed) + len(db.page.temp)) * BTREE_PAGE_SIZE
	if err := extendMmap(db, size); err != nil {
		return err
	}
	offset := int64(db.page.flushed * BTREE_PAGE_SIZE)
	if err := pwritev(db.fd, db.page.temp, offset); err != nil {
		return fmt.Errorf("pwritev: %w", err)
	}
	db.page.flushed += uint64(len(db.page.temp))
	db.dropTemp()
	return nil
}

func updateRoot(db *KV) error {
	if err := pwriteFull(db.fd, saveMeta(db), 0); err != nil {
		return fmt.Errorf("write meta page: %w", err)
	}
	return nil
}

func updateFile(db *KV) error {
	if err := writePages(db); err != nil {
		return err
	}
	if err := db.sync(); err != nil {
		return err
	}
	if err := updateRoot(db); err != nil {
		return err
	}
	return db.sync()
}

func updateOrRevert(db *KV, meta []byte) error {
	revert := func() {
		loadMeta(db, meta)
		db.dropTemp()
	}
	if db.failed {

		if err := pwriteFull(db.fd, meta, 0); err != nil {
			revert()
			return fmt.Errorf("repair meta page: %w", err)
		}
		if err := db.sync(); err != nil {
			revert()
			return err
		}
		db.failed = false
	}
	if err := updateFile(db); err != nil {
		db.failed = true
		revert()
		return err
	}
	return nil
}

func (db *KV) Get(key []byte) ([]byte, bool) { return db.tree.Get(key) }

func (db *KV) Scan(start []byte, fn func(key, val []byte) bool) { db.tree.Scan(start, fn) }

func (db *KV) Set(key, val []byte) error {
	if db.inTx {
		return db.tree.Insert(key, val)
	}
	meta := saveMeta(db)
	if err := db.tree.Insert(key, val); err != nil {
		return err
	}
	return updateOrRevert(db, meta)
}

func (db *KV) Del(key []byte) (bool, error) {
	if db.inTx {
		return db.tree.Delete(key)
	}
	meta := saveMeta(db)
	deleted, err := db.tree.Delete(key)
	if err != nil {
		return false, err
	}
	if !deleted {
		return false, nil
	}
	return true, updateOrRevert(db, meta)
}

func (db *KV) InTx() bool { return db.inTx }

func (db *KV) Begin() error {
	if db.inTx {
		return errors.New("transaction already active")
	}
	db.inTx = true
	db.txMeta = saveMeta(db)
	return nil
}

func (db *KV) Commit() error {
	if !db.inTx {
		return errors.New("no active transaction")
	}
	db.inTx = false
	if len(db.page.temp) == 0 {
		return nil
	}
	return updateOrRevert(db, db.txMeta)
}

func (db *KV) Rollback() error {
	if !db.inTx {
		return errors.New("no active transaction")
	}
	db.inTx = false
	loadMeta(db, db.txMeta)
	db.dropTemp()
	return nil
}

type savepoint struct {
	root  uint64
	ntemp int
}

func (db *KV) Savepoint() savepoint {

	db.free = append(db.free, db.pinned...)
	db.pinned = db.pinned[:0]
	db.spN = len(db.page.temp)
	return savepoint{db.tree.root, len(db.page.temp)}
}

func (db *KV) RollbackTo(sp savepoint) {
	db.tree.root = sp.root
	db.page.temp = db.page.temp[:sp.ntemp]
	db.pinned = db.pinned[:0]
	kept := db.free[:0]
	for _, ptr := range db.free {
		if int(ptr-db.page.flushed) < sp.ntemp {
			kept = append(kept, ptr)
		}
	}
	db.free = kept
}

func (db *KV) ReleaseSavepoint() {
	db.free = append(db.free, db.pinned...)
	db.pinned = db.pinned[:0]
	db.spN = 0
}
