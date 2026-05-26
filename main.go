package main

func main() {
	node := BNode(make([]byte, BTREE_PAGE_SIZE))
	node.setHeader(BNODE_LEAF, 2)

}
