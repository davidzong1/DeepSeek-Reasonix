// Package store is the file+frontmatter truth source for knowledge items.
// Item files live under <DataRoot>/<team-id>/items/<item-id>.md; every write is
// atomic (temp+rename) and item content is create-only per id. The store
// imports only leaves (fileutil, frontmatter) plus the standard library.
package store
