mkdir -p build

if ! command -v go &> /dev/null; then
  echo "Go command not found. Activating with 'gvm use go1.25.0'"
  gvm use go1.25.0
fi

go build -o ./build/sort_by_date src/cmd/sort_by_date.go
