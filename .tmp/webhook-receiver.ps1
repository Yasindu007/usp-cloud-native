$ErrorActionPreference = "Stop"

$prefix = "http://localhost:9092/"
$logFile = Join-Path (Resolve-Path ".tmp") "webhook-receiver-log.jsonl"
$stateFile = Join-Path (Resolve-Path ".tmp") "webhook-receiver-state.json"

if (Test-Path $logFile) {
    Remove-Item -LiteralPath $logFile -Force
}

$listener = [System.Net.HttpListener]::new()
$listener.Prefixes.Add($prefix)
$listener.Start()

$state = @{
    fail_once = @{}
}

try {
    while ($listener.IsListening) {
        $context = $listener.GetContext()
        $request = $context.Request
        $response = $context.Response

        $reader = New-Object System.IO.StreamReader($request.InputStream, $request.ContentEncoding)
        $body = $reader.ReadToEnd()
        $reader.Dispose()

        $path = $request.Url.AbsolutePath.Trim("/")
        $count = 0
        if ($state.fail_once.ContainsKey($path)) {
            $count = [int]$state.fail_once[$path]
        }
        $count++
        $state.fail_once[$path] = $count

        $entry = [ordered]@{
            received_at = [DateTime]::UtcNow.ToString("o")
            method = $request.HttpMethod
            path = $request.Url.AbsolutePath
            query = $request.Url.Query
            headers = @{
                "X-Webhook-ID" = $request.Headers["X-Webhook-ID"]
                "X-Webhook-Delivery" = $request.Headers["X-Webhook-Delivery"]
                "X-Webhook-Event" = $request.Headers["X-Webhook-Event"]
                "X-Webhook-Signature" = $request.Headers["X-Webhook-Signature"]
                "User-Agent" = $request.Headers["User-Agent"]
                "Content-Type" = $request.Headers["Content-Type"]
            }
            body = $body
            attempt = $count
        }
        ($entry | ConvertTo-Json -Compress -Depth 10) | Add-Content -LiteralPath $logFile
        ($state | ConvertTo-Json -Compress -Depth 10) | Set-Content -LiteralPath $stateFile

        $statusCode = 200
        $resp = @{ status = "ok"; attempt = $count }
        if ($path -eq "fail-once" -and $count -eq 1) {
            $statusCode = 500
            $resp = @{ status = "fail_once"; attempt = $count }
        }

        $payload = [System.Text.Encoding]::UTF8.GetBytes(($resp | ConvertTo-Json -Compress))
        $response.StatusCode = $statusCode
        $response.ContentType = "application/json"
        $response.OutputStream.Write($payload, 0, $payload.Length)
        $response.Close()
    }
}
finally {
    if ($listener.IsListening) {
        $listener.Stop()
    }
    $listener.Close()
}
