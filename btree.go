package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	BNODE_NODE = 1
	BNODE_LEAF = 2
	HEADER     = 4
)

const BTREE_PAGE_SIZE = 4096
const BTREE_MAX_KEY_SIZE = 1000
const BTREE_MAX_VAL_SIZE = 3000
const DB_SIG = "BuildYourOwnDB06"

type BNode []byte

func assert(cond bool) {
	if !cond {
		panic("assertion failed")
	}
}

func init() {
	node1max := 4 + 1*8 + 1*2 + 4 + BTREE_MAX_KEY_SIZE + BTREE_MAX_VAL_SIZE
	assert(node1max <= BTREE_PAGE_SIZE)
}

func (node BNode) btype() uint16 {
	return binary.LittleEndian.Uint16(node[0:2])
}
func (node BNode) nkeys() uint16 {
	return binary.LittleEndian.Uint16(node[2:4])
}
func (node BNode) setHeader(btype uint16, nkeys uint16) {
	binary.LittleEndian.PutUint16(node[0:2], btype)
	binary.LittleEndian.PutUint16(node[2:4], nkeys)
}
func (node BNode) getPtr(idx uint16) uint64 {
	assert(idx < node.nkeys())
	pos := 4 + 8*idx
	return binary.LittleEndian.Uint64(node[pos:])
}
func (node BNode) setPtr(idx uint16, val uint64) {
	assert(idx < node.nkeys())
	pos := 4 + 8*idx
	binary.LittleEndian.PutUint64(node[pos:], val)
}
func (node BNode) getOffset(idx uint16) uint16 {
	if idx == 0 {
		return 0
	}
	pos := 4 + 8*node.nkeys() + 2*(idx-1)
	return binary.LittleEndian.Uint16(node[pos:])
}
func (node BNode) setOffset(idx uint16, offset uint16) {
	if idx == 0 {
		return
	}
	pos := 4 + 8*node.nkeys() + 2*(idx-1)
	binary.LittleEndian.PutUint16(node[pos:], offset)
}
func (node BNode) kvPos(idx uint16) uint16 {
	assert(idx <= node.nkeys())
	return 4 + 8*node.nkeys() + 2*node.nkeys() + node.getOffset(idx)
}
func (node BNode) getKey(idx uint16) []byte {
	assert(idx < node.nkeys())
	pos := node.kvPos(idx)
	klen := binary.LittleEndian.Uint16(node[pos:])
	return node[pos+4:][:klen]
}
func (node BNode) getVal(idx uint16) []byte {
	assert(idx < node.nkeys())
	pos := node.kvPos(idx)
	klen := binary.LittleEndian.Uint16(node[pos+0:])
	vlen := binary.LittleEndian.Uint16(node[pos+2:])
	return node[pos+4+klen:][:vlen]
}
func (node BNode) nbytes() uint16 {
	return node.kvPos(node.nkeys())
}

func nodeAppendKV(new BNode, idx uint16, ptr uint64, key []byte, val []byte) {
	new.setPtr(idx, ptr)
	pos := new.kvPos(idx)
	binary.LittleEndian.PutUint16(new[pos+0:], uint16(len(key)))
	binary.LittleEndian.PutUint16(new[pos+2:], uint16(len(val)))
	copy(new[pos+4:], key)
	copy(new[pos+4+uint16(len(key)):], val)
	new.setOffset(idx+1, new.getOffset(idx)+4+uint16(len(key)+len(val)))
}
func nodeAppendRange(new BNode, old BNode, dstNew uint16, srcOld uint16, n uint16) {
	for i := uint16(0); i < n; i++ {
		dst, src := dstNew+i, srcOld+i
		nodeAppendKV(new, dst, old.getPtr(src), old.getKey(src), old.getVal(src))
	}
}
func leafInsert(new BNode, old BNode, idx uint16, key []byte, val []byte) {
	new.setHeader(BNODE_LEAF, old.nkeys()+1)
	nodeAppendRange(new, old, 0, 0, idx)
	nodeAppendKV(new, idx, 0, key, val)
	nodeAppendRange(new, old, idx+1, idx, old.nkeys()-idx)
}
func leafUpdate(new BNode, old BNode, idx uint16, key []byte, val []byte) {
	new.setHeader(BNODE_LEAF, old.nkeys())
	nodeAppendRange(new, old, 0, 0, idx)
	nodeAppendKV(new, idx, 0, key, val)
	nodeAppendRange(new, old, idx+1, idx+1, old.nkeys()-(idx+1))
}
func leafDelete(new BNode, old BNode, idx uint16) {
	new.setHeader(BNODE_LEAF, old.nkeys()-1)
	nodeAppendRange(new, old, 0, 0, idx)
	nodeAppendRange(new, old, idx, idx+1, old.nkeys()-(idx+1))
}
func nodeMerge(new BNode, left BNode, right BNode) {
	new.setHeader(left.btype(), left.nkeys()+right.nkeys())
	nodeAppendRange(new, left, 0, 0, left.nkeys())
	nodeAppendRange(new, right, left.nkeys(), 0, right.nkeys())
}
func nodeReplace2Kid(new BNode, old BNode, idx uint16, ptr uint64, key []byte) {
	new.setHeader(BNODE_NODE, old.nkeys()-1)
	nodeAppendRange(new, old, 0, 0, idx)
	nodeAppendKV(new, idx, ptr, key, nil)
	nodeAppendRange(new, old, idx+1, idx+2, old.nkeys()-(idx+2))
}
func nodeLookupLE(node BNode, key []byte) uint16 {
	nkeys := node.nkeys()
	var i uint16
	for i = 0; i < nkeys; i++ {
		cmp := bytes.Compare(node.getKey(i), key)
		if cmp == 0 {
			return i
		}
		if cmp > 0 {
			return i - 1
		}
	}
	return i - 1
}
func nodeSplit2(left BNode, right BNode, old BNode) {
	assert(old.nkeys() >= 2)
	nleft := old.nkeys() / 2
	left_bytes := func() uint16 {
		return 4 + 8*nleft + 2*nleft + old.getOffset(nleft)
	}
	for left_bytes() > BTREE_PAGE_SIZE {
		nleft--
	}
	assert(nleft >= 1)
	right_bytes := func() uint16 {
		return old.nbytes() - left_bytes() + 4
	}
	for right_bytes() > BTREE_PAGE_SIZE {
		nleft++
	}
	assert(nleft < old.nkeys())
	nright := old.nkeys() - nleft
	left.setHeader(old.btype(), nleft)
	right.setHeader(old.btype(), nright)
	nodeAppendRange(left, old, 0, 0, nleft)
	nodeAppendRange(right, old, 0, nleft, nright)
	assert(right.nbytes() <= BTREE_PAGE_SIZE)
}
func nodeSplit3(old BNode) (uint16, [3]BNode) {
	if old.nbytes() <= BTREE_PAGE_SIZE {
		old = old[:BTREE_PAGE_SIZE]
		return 1, [3]BNode{old}
	}
	left := BNode(make([]byte, 2*BTREE_PAGE_SIZE))
	right := BNode(make([]byte, BTREE_PAGE_SIZE))
	nodeSplit2(left, right, old)
	if left.nbytes() <= BTREE_PAGE_SIZE {
		left = left[:BTREE_PAGE_SIZE]
		return 2, [3]BNode{left, right}
	}
	leftleft := BNode(make([]byte, BTREE_PAGE_SIZE))
	middle := BNode(make([]byte, BTREE_PAGE_SIZE))
	nodeSplit2(leftleft, middle, left)
	assert(leftleft.nbytes() <= BTREE_PAGE_SIZE)
	return 3, [3]BNode{leftleft, middle, right}
}

type BTree struct {
	root uint64
	get  func(uint64) []byte
	new  func([]byte) uint64
	del  func(uint64)
}

func treeInsert(tree *BTree, node BNode, key []byte, val []byte) BNode {
	new := BNode(make([]byte, 2*BTREE_PAGE_SIZE))
	idx := nodeLookupLE(node, key)
	switch node.btype() {
	case BNODE_LEAF:
		if bytes.Equal(key, node.getKey(idx)) {
			leafUpdate(new, node, idx, key, val)
		} else {
			leafInsert(new, node, idx+1, key, val)
		}
	case BNODE_NODE:
		kptr := node.getPtr(idx)
		knode := treeInsert(tree, tree.get(kptr), key, val)
		nsplit, split := nodeSplit3(knode)
		tree.del(kptr)
		nodeReplaceKidN(tree, new, node, idx, split[:nsplit]...)
	}
	return new
}
func nodeReplaceKidN(tree *BTree, new BNode, old BNode, idx uint16, kids ...BNode) {
	inc := uint16(len(kids))
	new.setHeader(BNODE_NODE, old.nkeys()+inc-1)
	nodeAppendRange(new, old, 0, 0, idx)
	for i, node := range kids {
		nodeAppendKV(new, idx+uint16(i), tree.new(node), node.getKey(0), nil)
	}
	nodeAppendRange(new, old, idx+inc, idx+1, old.nkeys()-(idx+1))
}
func checkLimit(key []byte, val []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("key is empty")
	}
	if len(key) > BTREE_MAX_KEY_SIZE {
		return fmt.Errorf("key is too long")
	}
	if len(val) > BTREE_MAX_VAL_SIZE {
		return fmt.Errorf("val is too long")
	}
	return nil
}
func (tree *BTree) Get(key []byte) ([]byte, bool) {
	if tree.root == 0 {
		return nil, false
	}
	node := BNode(tree.get(tree.root))
	for {
		idx := nodeLookupLE(node, key)
		switch node.btype() {
		case BNODE_LEAF:
			if bytes.Equal(key, node.getKey(idx)) {
				return node.getVal(idx), true
			}
			return nil, false
		case BNODE_NODE:
			node = BNode(tree.get(node.getPtr(idx)))
		}
	}
}
func (tree *BTree) Insert(key []byte, val []byte) error {
	if err := checkLimit(key, val); err != nil {
		return err
	}
	if tree.root == 0 {
		root := BNode(make([]byte, BTREE_PAGE_SIZE))
		root.setHeader(BNODE_LEAF, 2)
		nodeAppendKV(root, 0, 0, nil, nil)
		nodeAppendKV(root, 1, 0, key, val)
		tree.root = tree.new(root)
		return nil
	}
	node := treeInsert(tree, tree.get(tree.root), key, val)
	nsplit, split := nodeSplit3(node)
	tree.del(tree.root)
	if nsplit > 1 {
		root := BNode(make([]byte, BTREE_PAGE_SIZE))
		root.setHeader(BNODE_NODE, nsplit)
		for i, knode := range split[:nsplit] {
			ptr, key := tree.new(knode), knode.getKey(0)
			nodeAppendKV(root, uint16(i), ptr, key, nil)
		}
		tree.root = tree.new(root)
	} else {
		tree.root = tree.new(split[0])
	}
	return nil
}
func (tree *BTree) Delete(key []byte) (bool, error) {
	if tree.root == 0 {
		return false, nil
	}
	updated := treeDelete(tree, tree.get(tree.root), key)
	if len(updated) == 0 {
		return false, nil
	}
	tree.del(tree.root)
	if updated.btype() == BNODE_NODE && updated.nkeys() == 1 {
		tree.root = updated.getPtr(0)
	} else {
		tree.root = tree.new(updated)
	}
	return true, nil
}
func shouldMerge(tree *BTree, node BNode, idx uint16, updated BNode) (int, BNode) {
	if updated.nbytes() > BTREE_PAGE_SIZE/4 {
		return 0, BNode{}
	}
	if idx > 0 {
		sibling := BNode(tree.get(node.getPtr(idx - 1)))
		merged := sibling.nbytes() + updated.nbytes() - HEADER
		if merged <= BTREE_PAGE_SIZE {
			return -1, sibling
		}
	}
	if idx+1 < node.nkeys() {
		sibling := BNode(tree.get(node.getPtr(idx + 1)))
		merged := sibling.nbytes() + updated.nbytes() - HEADER
		if merged <= BTREE_PAGE_SIZE {
			return +1, sibling
		}
	}
	return 0, BNode{}
}
func treeDelete(tree *BTree, node BNode, key []byte) BNode {
	idx := nodeLookupLE(node, key)
	switch node.btype() {
	case BNODE_LEAF:
		if !bytes.Equal(key, node.getKey(idx)) {
			return BNode{}
		}
		new := BNode(make([]byte, BTREE_PAGE_SIZE))
		leafDelete(new, node, idx)
		return new
	case BNODE_NODE:
		return nodeDelete(tree, node, idx, key)
	}
	panic("bad node type")
}
func nodeDelete(tree *BTree, node BNode, idx uint16, key []byte) BNode {
	kptr := node.getPtr(idx)
	updated := treeDelete(tree, tree.get(kptr), key)
	if len(updated) == 0 {
		return BNode{}
	}
	tree.del(kptr)
	new := BNode(make([]byte, BTREE_PAGE_SIZE))
	mergeDir, sibling := shouldMerge(tree, node, idx, updated)
	switch {
	case mergeDir < 0:
		merged := BNode(make([]byte, BTREE_PAGE_SIZE))
		nodeMerge(merged, sibling, updated)
		tree.del(node.getPtr(idx - 1))
		nodeReplace2Kid(new, node, idx-1, tree.new(merged), merged.getKey(0))
	case mergeDir > 0:
		merged := BNode(make([]byte, BTREE_PAGE_SIZE))
		nodeMerge(merged, updated, sibling)
		tree.del(node.getPtr(idx + 1))
		nodeReplace2Kid(new, node, idx, tree.new(merged), merged.getKey(0))
	case mergeDir == 0 && updated.nkeys() == 0:
		assert(node.nkeys() == 1 && idx == 0)
		new.setHeader(BNODE_NODE, 0)
	case mergeDir == 0 && updated.nkeys() > 0:
		nodeReplaceKidN(tree, new, node, idx, updated)
	}
	return new
}

type C struct {
	tree  BTree
	ref   map[string]string
	pages map[uint64]BNode
}

func newC() *C {
	pages := map[uint64]BNode{}
	return &C{
		tree: BTree{
			get: func(ptr uint64) []byte {
				node, ok := pages[ptr]
				assert(ok)
				return node
			},
			new: func(node []byte) uint64 {
				assert(BNode(node).nbytes() <= BTREE_PAGE_SIZE)
				ptr := uint64(uintptr(unsafe.Pointer(&node[0])))
				assert(pages[ptr] == nil)
				pages[ptr] = node
				return ptr
			},
			del: func(ptr uint64) {
				assert(pages[ptr] != nil)
				delete(pages, ptr)
			},
		},
		ref:   map[string]string{},
		pages: pages,
	}
}
func (c *C) add(key string, val string) {
	c.tree.Insert([]byte(key), []byte(val))
	c.ref[key] = val
}
func (c *C) del(key string) bool {
	deleted, _ := c.tree.Delete([]byte(key))
	if deleted {
		delete(c.ref, key)
	}
	return deleted
}

type KV struct {
	Path   string
	fd     int
	tree   BTree
	failed bool
	mmap   struct {
		total  int
		chunks [][]byte
	}
	page struct {
		flushed uint64
		temp    [][]byte
	}
}

func (db *KV) pageRead(ptr uint64) []byte {
	start := uint64(0)
	for _, chunk := range db.mmap.chunks {
		end := start + uint64(len(chunk))/BTREE_PAGE_SIZE
		if ptr < end {
			offset := BTREE_PAGE_SIZE * (ptr - start)
			return chunk[offset : offset+BTREE_PAGE_SIZE]
		}
		start = end
	}
	panic("bad ptr")
}

func (db *KV) pageAppend(node []byte) uint64 {
	ptr := db.page.flushed + uint64(len(db.page.temp))
	db.page.temp = append(db.page.temp, node)
	return ptr
}

func extendMmap(db *KV, size int) error {
	if size <= db.mmap.total {
		return nil
	}
	alloc := max(db.mmap.total, 64<<20)
	for db.mmap.total+alloc < size {
		alloc *= 2
	}
	chunk, err := syscall.Mmap(
		db.fd, int64(db.mmap.total), alloc,
		syscall.PROT_READ, syscall.MAP_SHARED,
	)
	if err != nil {
		return fmt.Errorf("mmap: %w", err)
	}
	db.mmap.total += alloc
	db.mmap.chunks = append(db.mmap.chunks, chunk)
	return nil
}

func saveMeta(db *KV) []byte {
	var data [32]byte
	copy(data[:16], []byte(DB_SIG))
	binary.LittleEndian.PutUint64(data[16:], db.tree.root)
	binary.LittleEndian.PutUint64(data[24:], db.page.flushed)
	return data[:]
}

func loadMeta(db *KV, data []byte) {
	db.tree.root = binary.LittleEndian.Uint64(data[16:])
	db.page.flushed = binary.LittleEndian.Uint64(data[24:])
}

func readRoot(db *KV, fileSize int64) error {
	if fileSize == 0 {
		db.page.flushed = 1
		return nil
	}
	data := db.mmap.chunks[0]
	loadMeta(db, data)
	return nil
}

func writePages(db *KV) error {
	size := (int(db.page.flushed) + len(db.page.temp)) * BTREE_PAGE_SIZE
	if err := extendMmap(db, size); err != nil {
		return err
	}
	offset := int64(db.page.flushed * BTREE_PAGE_SIZE)
	if _, err := unix.Pwritev(db.fd, db.page.temp, offset); err != nil {
		return err
	}
	db.page.flushed += uint64(len(db.page.temp))
	db.page.temp = db.page.temp[:0]
	return nil
}

func updateRoot(db *KV) error {
	if _, err := syscall.Pwrite(db.fd, saveMeta(db), 0); err != nil {
		return fmt.Errorf("write meta page: %w", err)
	}
	return nil
}

func updateFile(db *KV) error {
	if err := writePages(db); err != nil {
		return err
	}
	if err := syscall.Fsync(db.fd); err != nil {
		return err
	}
	if err := updateRoot(db); err != nil {
		return err
	}
	return syscall.Fsync(db.fd)
}

func updateOrRevert(db *KV, meta []byte) error {
	if db.failed {
		if err := updateRoot(db); err != nil {
			return err
		}
		if err := syscall.Fsync(db.fd); err != nil {
			return err
		}
		db.failed = false
	}
	err := updateFile(db)
	if err != nil {
		loadMeta(db, meta)
		db.page.temp = db.page.temp[:0]
		db.failed = true
	}
	return err
}

func (db *KV) Get(key []byte) ([]byte, bool) {
	return db.tree.Get(key)
}

func (db *KV) Set(key []byte, val []byte) error {
	meta := saveMeta(db)
	if err := db.tree.Insert(key, val); err != nil {
		return err
	}
	return updateOrRevert(db, meta)
}

func (db *KV) Del(key []byte) (bool, error) {
	meta := saveMeta(db)
	deleted, err := db.tree.Delete(key)
	if err != nil {
		return false, err
	}
	return deleted, updateOrRevert(db, meta)
}

func (db *KV) Open() error {
	fd, err := createFileSync(db.Path)
	if err != nil {
		return fmt.Errorf("KV open: %w", err)
	}
	db.fd = fd
	var info syscall.Stat_t
	if err = syscall.Fstat(fd, &info); err != nil {
		return fmt.Errorf("fstat: %w", err)
	}
	fileSize := info.Size
	if fileSize > 0 {
		if err = extendMmap(db, int(fileSize)); err != nil {
			return err
		}
	}
	db.tree.get = db.pageRead
	db.tree.new = db.pageAppend
	db.tree.del = func(ptr uint64) {}
	return readRoot(db, fileSize)
}

func createFileSync(file string) (int, error) {
	flags := os.O_RDONLY | syscall.O_DIRECTORY
	dirfd, err := syscall.Open(path.Dir(file), flags, 0o644)
	if err != nil {
		return -1, fmt.Errorf("open directory: %w", err)
	}
	defer syscall.Close(dirfd)
	flags = os.O_RDWR | os.O_CREATE
	fd, err := syscall.Openat(dirfd, path.Base(file), flags, 0o644)
	if err != nil {
		return -1, fmt.Errorf("open file: %w", err)
	}
	if err = syscall.Fsync(dirfd); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("fsync: %w", err)
	}
	return fd, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	c := newC()
	c.add("hello", "world")
	c.add("foo", "bar")
	c.add("apple", "juice")
	val, ok := c.tree.Get([]byte("hello"))
	fmt.Printf("get hello: %s, found: %v\n", val, ok)
	c.del("foo")
	_, ok = c.tree.Get([]byte("foo"))
	fmt.Printf("get foo after delete, found: %v\n", ok)
}
