// Package queue is the durable, single-writer job log for a team knowledge
// base. Events append to an fsynced events.log; a cursor file records the last
// fully confirmed sequence. At-least-once consumers replay events above the
// cursor and rely on idempotent side effects (create-only item writes).
package queue
