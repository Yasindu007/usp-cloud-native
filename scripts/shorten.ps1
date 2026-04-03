param(
  [Parameter(Mandatory = $true)]
  [string]$Token,
  [string]$OriginalUrl = "https://github.com/golang/go",
  [string]$Title = "Go Repo"
)

$body = ('{{"original_url":"{0}","title":"{1}"}}' -f $OriginalUrl, $Title)

curl.exe -s -X POST http://localhost:8080/api/v1/urls `
  -H "Authorization: Bearer $Token" `
  -H "Content-Type: application/json" `
  --data-binary $body
