package api

// unregisteredRoutes are the routes still mounted without endpoint.Register, by path. Each
// area issue (#630 to #652) takes its routes off as it registers them, and nothing is added:
// a new route is registered from the start (#669).
var unregisteredRoutes = []string{}
