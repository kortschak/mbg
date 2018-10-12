# mbg

`mbg` is an mbox forensics tool written for fun. It reads an email mbox file and constructs a multigraph of co-appearing email addresses and then writes out either a simple graph in DOT format or a time-dynamic multigraph in GEXF format.

The resulting graph file can be used to perform graph visualisation with tools such as Gephi.

## Installation

```
go get github.com/kortschak/mbg
```

