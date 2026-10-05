package platform

// httpapi imports platform, so tests that serve the router live in package
// platform_test and reach these test helpers through here.
var RedisForTest = testRedis

const UnusedRedisURL = unusedRedis
