package qbit

import "io"

// LibraryLogWriter expose aux tests la destination du journal remis à la bibliothèque.
func LibraryLogWriter(client *Client) io.Writer { return client.libraryLog.Writer() }
