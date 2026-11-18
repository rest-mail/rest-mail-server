package db

// ConnectWithClock opens a connection whose timestamps come from the given clock, so a
// test can use one finer than the platform's: macOS reads only microseconds.
var ConnectWithClock = connect
