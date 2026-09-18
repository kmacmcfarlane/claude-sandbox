package cascade

// Test-only exports for the external cascade_test package.

// ReadEnvAssignments exposes the shared env-file reader so tests can assert
// the values it yields, not only the lint output built on them.
var ReadEnvAssignments = readEnvAssignments
