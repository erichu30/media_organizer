mkdir -p build

if ! command -v go &> /dev/null; then
  if ! command -v gvm &> /dev/null; then
    echo "Go command not found and gvm is not installed. Please install Go or gvm."
    exit 1
  fi
  echo "Go command not found. Activating with gvm"
  gvm install go1.25.0
  gvm use go1.25.0
fi

go build -o ./build/sort_by_date src/cmd/sort_by_date.go
