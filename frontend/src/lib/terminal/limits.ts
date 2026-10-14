// Each terminal holds one HTTP stream and HTTP/1.1 allows about six per
// origin, which the app also needs for runs and the status watch.
export const MAX_TERMINALS = 3
