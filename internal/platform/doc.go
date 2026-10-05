// Package platform holds the process-wide plumbing every domain shares:
// configuration, logging, the database pool, the Redis client, and the task
// queue and worker; the clock as it arrives.
//
// Domain functions that cause background work return the tasks to enqueue,
// and the handler passes them to Queue.Enqueue after the transaction commits.
// Nothing enqueues inside a transaction: a task enqueued before a rollback
// would run against a row that never existed, and one enqueued before the
// commit could run before the row is visible.
package platform
