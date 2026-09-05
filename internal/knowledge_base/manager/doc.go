// Package manager is the single facade a host uses for one team's knowledge
// base. It funnels every write through a durable, single-consumer queue so item
// commits stay ordered and idempotent; reads hit an in-memory read model.
package manager
