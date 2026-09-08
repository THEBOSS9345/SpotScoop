// This directory holds the React frontend and contains no Go source of its own.
//
// It exists solely so the root module's ./... pattern skips this subtree: npm
// installs packages that ship Go files (e.g. flatted/golang), and without a
// nested module boundary those get picked up by every build, vet and test run.
module spotscoop/frontend

go 1.26.3
