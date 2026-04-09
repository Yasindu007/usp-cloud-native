$ErrorActionPreference = "Stop"

function New-HmacSignature {
    param(
        [Parameter(Mandatory = $true)][string]$Secret,
        [Parameter(Mandatory = $true)][string]$Body
    )

    $hmac = [System.Security.Cryptography.HMACSHA256]::new([System.Text.Encoding]::UTF8.GetBytes($Secret))
    try {
        $hash = $hmac.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($Body))
    }
    finally {
        $hmac.Dispose()
    }

    return "sha256=" + (-join ($hash | ForEach-Object { $_.ToString("x2") }))
}

$root = "E:\usp-cloud-native"
$logFile = Join-Path $root ".tmp\webhook-receiver-log.jsonl"
if (Test-Path $logFile) {
    Remove-Item -LiteralPath $logFile -Force
}

$jobs = @()
$jobs += Start-Job -Name "mockissuer" -ScriptBlock {
    Set-Location "E:\usp-cloud-native"
    go run ./cmd/mockissuer
}
$jobs += Start-Job -Name "api" -ScriptBlock {
    Set-Location "E:\usp-cloud-native"
    go run ./cmd/api
}
$jobs += Start-Job -Name "redirector" -ScriptBlock {
    Set-Location "E:\usp-cloud-native"
    go run ./cmd/redirector
}
$jobs += Start-Job -Name "receiver" -ScriptBlock {
    Set-Location "E:\usp-cloud-native"
    go run .tmp/webhook_receiver.go
}

try {
    $healthChecks = @(
        "http://localhost:9000/healthz",
        "http://localhost:8080/healthz",
        "http://localhost:8081/healthz"
    )

    foreach ($health in $healthChecks) {
        $ready = $false
        for ($i = 0; $i -lt 60; $i++) {
            Start-Sleep -Seconds 1
            try {
                Invoke-RestMethod -Method Get -Uri $health -TimeoutSec 5 | Out-Null
                $ready = $true
                break
            }
            catch {
            }
        }
        if (-not $ready) {
            throw "service did not become healthy: $health"
        }
    }

    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Seconds 1
        try {
            Invoke-RestMethod -Method Post -Uri "http://localhost:9092/health" -Body "{}" -ContentType "application/json" | Out-Null
            $ready = $true
            break
        }
        catch {
        }
    }

    if (-not $ready) {
        throw "webhook receiver did not start"
    }

    $tokenResp = Invoke-RestMethod -Method Post -Uri "http://localhost:9000/token" -Body @{
        grant_type = "client_credentials"
        client_id = "dev"
        workspace_id = "ws_default"
        user_id = "usr_default"
        scope = "read write"
    }
    $token = $tokenResp.access_token

    $headers = @{
        Authorization = "Bearer $token"
        "Content-Type" = "application/json"
    }

    $workspaceID = "ws_default"

    $tokenResp2 = Invoke-RestMethod -Method Post -Uri "http://localhost:9000/token" -Body @{
        grant_type = "client_credentials"
        client_id = "dev"
        workspace_id = $workspaceID
        user_id = "usr_default"
        scope = "read write"
    }
    $token2 = $tokenResp2.access_token
    $headers2 = @{
        Authorization = "Bearer $token2"
        "Content-Type" = "application/json"
    }

    $baseWs = "http://localhost:8080/api/v1/workspaces/$workspaceID"

    $createWebhookResp = Invoke-RestMethod -Method Post -Uri "$baseWs/webhooks" -Headers $headers2 -Body '{"name":"create-fail-once","url":"http://localhost:9092/fail-once","events":["url.created"]}'
    $createSecret = $createWebhookResp.data.secret

    $redirectWebhookResp = Invoke-RestMethod -Method Post -Uri "$baseWs/webhooks" -Headers $headers2 -Body '{"name":"redirect-ok","url":"http://localhost:9092/ok","events":["redirect.received"]}'
    $redirectSecret = $redirectWebhookResp.data.secret

    $url1Resp = Invoke-RestMethod -Method Post -Uri "$baseWs/urls" -Headers $headers2 -Body '{"original_url":"https://example.com/retry-check"}'
    $url2Resp = Invoke-RestMethod -Method Post -Uri "$baseWs/urls" -Headers $headers2 -Body '{"original_url":"https://example.com/redirect-check"}'
    $shortCode = $url2Resp.data.short_code

    Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:8081/$shortCode" -MaximumRedirection 0 -ErrorAction SilentlyContinue | Out-Null

    $createEntries = @()
    $redirectEntries = @()
    for ($i = 0; $i -lt 50; $i++) {
        Start-Sleep -Seconds 1
        if (Test-Path $logFile) {
            $lines = Get-Content $logFile | Where-Object { $_ -ne "" }
            $entries = @($lines | ForEach-Object { $_ | ConvertFrom-Json })
            $createEntries = @($entries | Where-Object { $_.path -eq "/fail-once" -and $_.headers."X-Webhook-Event" -eq "url.created" })
            $redirectEntries = @($entries | Where-Object { $_.path -eq "/ok" -and $_.headers."X-Webhook-Event" -eq "redirect.received" })
            if ($createEntries.Count -ge 2 -and $redirectEntries.Count -ge 1) {
                break
            }
        }
    }

    if ($createEntries.Count -lt 2) {
        throw "did not observe webhook retry for url.created"
    }
    if ($redirectEntries.Count -lt 1) {
        throw "did not observe redirect.received delivery"
    }

    $createSigExpected = New-HmacSignature -Secret $createSecret -Body $createEntries[0].body
    $redirectSigExpected = New-HmacSignature -Secret $redirectSecret -Body $redirectEntries[0].body

    [pscustomobject]@{
        migration_version = 8
        workspace_id = $workspaceID
        create_webhook_id = $createWebhookResp.data.id
        redirect_webhook_id = $redirectWebhookResp.data.id
        retry_attempts = $createEntries.Count
        create_signature_valid = ($createEntries[0].headers."X-Webhook-Signature" -eq $createSigExpected)
        redirect_signature_valid = ($redirectEntries[0].headers."X-Webhook-Signature" -eq $redirectSigExpected)
        redirect_delivery_seen = $true
        create_first_attempt_path = $createEntries[0].path
        redirect_first_attempt_path = $redirectEntries[0].path
    } | ConvertTo-Json -Depth 5
}
finally {
    foreach ($job in $jobs) {
        if ($job) {
            Stop-Job $job -ErrorAction SilentlyContinue | Out-Null
            Remove-Job $job -Force -ErrorAction SilentlyContinue | Out-Null
        }
    }
}
