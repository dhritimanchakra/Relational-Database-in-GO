package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	BNODE_NODE = 1
	BNODE_LEAF = 2
	HEADER     = 4
)

const BTREE_PAGE_SIZE = 4096
const BTREE_MAX_KEY_SIZE = 1000
const BTREE_MAX_VAL_SIZE = 3000

type BNode []byte

func assert(cond bool) {
	if !cond {
		panic("assertion failed")
	}
}

func init() {
	node1max := HEADER + 1*8 + 1*2 + 4 + BTREE_MAX_KEY_SIZE + BTREE_MAX_VAL_SIZE
	assert(node1max <= BTREE_PAGE_SIZE)
}

func (node BNode) btype() uint16 { return binary.LittleEndian.Uint16(node[0:2]) }
func (node BNode) nkeys() uint16 { return binary.LittleEndian.Uint16(node[2:4]) }

func (node BNode) setHeader(btype uint16, nkeys uint16) {
	binary.LittleEndian.PutUint16(node[0:2], btype)
	binary.LittleEndian.PutUint16(node[2:4], nkeys)
}

func (node BNode) getPtr(idx uint16) uint64 {
	assert(idx < node.nkeys())
	pos := HEADER + 8*idx
	return binary.LittleEndian.Uint64(node[pos:])
}

func (node BNode) setPtr(idx uint16, val uint64) {
	assert(idx < node.nkeys())
	pos := HEADER + 8*idx
	binary.LittleEndian.PutUint64(node[pos:], val)
}

func (node BNode) getOffset(idx uint16) uint16 {
	if idx == 0 {
		return 0
	}
	pos := HEADER + 8*node.nkeys() + 2*(idx-1)
	return binary.LittleEndian.Uint16(node[pos:])
}

func (node BNode) setOffset(idx uint16, offset uint16) {
	if idx == 0 {
		return
	}
	pos := HEADER + 8*node.nkeys() + 2*(idx-1)
	binary.LittleEndian.PutUint16(node[pos:], offset)
}

func (node BNode) kvPos(idx uint16) uint16 {
	assert(idx <= node.nkeys())
	return HEADER + 8*node.nkeys() + 2*node.nkeys() + node.getOffset(idx)
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

func (node BNode) nbytes() uint16 { return node.kvPos(node.nkeys()) }

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
		return HEADER + 8*nleft + 2*nleft + old.getOffset(nleft)
	}
	for left_bytes() > BTREE_PAGE_SIZE {
		nleft--
	}
	assert(nleft >= 1)
	right_bytes := func() uint16 {
		return old.nbytes() - left_bytes() + HEADER
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
		return fmt.Errorf("key is too long (%d > %d bytes)", len(key), BTREE_MAX_KEY_SIZE)
	}
	if len(val) > BTREE_MAX_VAL_SIZE {
		return fmt.Errorf("value is too long (%d > %d bytes)", len(val), BTREE_MAX_VAL_SIZE)
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
	tree.installRoot(nsplit, split)
	return nil
}

func (tree *BTree) installRoot(nsplit uint16, split [3]BNode) {
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
}

func (tree *BTree) Delete(key []byte) (bool, error) {
	if len(key) == 0 {
		return false, fmt.Errorf("key is empty")
	}
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
		nsplit, split := nodeSplit3(updated)
		tree.installRoot(nsplit, split)
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

	new := BNode(make([]byte, 2*BTREE_PAGE_SIZE))
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
		nsplit, split := nodeSplit3(updated)
		nodeReplaceKidN(tree, new, node, idx, split[:nsplit]...)
	}
	return new
}

func (tree *BTree) Scan(start []byte, fn func(key, val []byte) bool) {
	if tree.root == 0 {
		return
	}
	tree.scanNode(BNode(tree.get(tree.root)), start, fn)
}

func (tree *BTree) scanNode(node BNode, start []byte, fn func(key, val []byte) bool) bool {
	n := node.nkeys()
	switch node.btype() {
	case BNODE_LEAF:
		for i := uint16(0); i < n; i++ {
			k := node.getKey(i)
			if len(k) == 0 || bytes.Compare(k, start) < 0 {
				continue
			}
			if !fn(k, node.getVal(i)) {
				return false
			}
		}
	case BNODE_NODE:
		first := nodeLookupLE(node, start)
		if first == ^uint16(0) {
			first = 0
		}
		for i := first; i < n; i++ {
			if !tree.scanNode(BNode(tree.get(node.getPtr(i))), start, fn) {
				return false
			}
		}
	}
	return true
}
