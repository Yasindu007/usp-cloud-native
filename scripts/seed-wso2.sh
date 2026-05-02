#!/usr/bin/env bash
# Import the local URL Shortener APIs into WSO2 API Manager.
#
# The script is intentionally implemented in Python's standard library so it
# runs in Git Bash, WSL, Linux, and macOS without jq.

set -euo pipefail

if command -v python3 >/dev/null 2>&1; then
  PYTHON_CMD=(python3)
elif command -v python >/dev/null 2>&1; then
  PYTHON_CMD=(python)
elif command -v py >/dev/null 2>&1; then
  PYTHON_CMD=(py -3)
else
  echo "ERROR: missing required command: python3, python, or py" >&2
  exit 1
fi

"${PYTHON_CMD[@]}" - "$@" <<'PY'
import base64
import json
import os
import shutil
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

WSO2_HOST = os.environ.get("WSO2_HOST", "https://localhost:9443").rstrip("/")
WSO2_USER = os.environ.get("WSO2_USER", "admin")
WSO2_PASS = os.environ.get("WSO2_PASS", "admin")
API_BACKEND_URL = os.environ.get("API_BACKEND_URL", "http://api.shortener.local/api/v1")
REDIRECT_BACKEND_URL = os.environ.get("REDIRECT_BACKEND_URL", "http://r.shortener.local")
MOCK_ISSUER_URL = os.environ.get("MOCK_ISSUER_URL", "http://127.0.0.1:9000").rstrip("/")
MOCK_ISSUER_PUBLIC_URL = os.environ.get("MOCK_ISSUER_PUBLIC_URL", "http://localhost:9000").rstrip("/")
MOCK_KEY_MANAGER_NAME = os.environ.get("MOCK_KEY_MANAGER_NAME", "URLShortener-MockIssuer")
RESET = len(sys.argv) > 1 and sys.argv[1] == "--reset"
CTX = ssl._create_unverified_context()
WINDOWS_CURL = shutil.which("curl.exe")


def log(msg):
    print(f"==> {msg}", flush=True)


def warn(msg):
    print(f"WARN: {msg}", file=sys.stderr, flush=True)


def fail(msg, body=""):
    print(f"ERROR: {msg}", file=sys.stderr)
    if body:
        print(body, file=sys.stderr)
    sys.exit(1)


def basic(user, password):
    raw = f"{user}:{password}".encode()
    return "Basic " + base64.b64encode(raw).decode()


def request(method, url, *, headers=None, json_body=None, form=None, allow_error=False, context=CTX):
    headers = dict(headers or {})
    data = None
    if json_body is not None:
        data = json.dumps(json_body).encode()
        headers.setdefault("Content-Type", "application/json")
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        headers.setdefault("Content-Type", "application/x-www-form-urlencoded")

    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, context=context, timeout=30) as resp:
            return resp.status, resp.read().decode(errors="replace")
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        if allow_error:
            return exc.code, body
        fail(f"{method} {url} failed with HTTP {exc.code}", body)
    except urllib.error.URLError as exc:
        if allow_error:
            return 0, str(exc)
        fail(f"{method} {url} failed", str(exc))


def get_json(method, url, **kwargs):
    status, body = request(method, url, **kwargs)
    try:
        return json.loads(body or "{}")
    except json.JSONDecodeError:
        fail(f"{method} {url} did not return JSON", body)


def parse_json_response(status, body, action):
    try:
        return json.loads(body or "{}")
    except json.JSONDecodeError:
        fail(f"{action} returned non-JSON HTTP {status}", body)


def wait_for_wso2():
    for attempt in range(1, 41):
        status, body = request("GET", f"{WSO2_HOST}/services/Version", allow_error=True)
        if status == 200 and "version" in body.lower():
            return
        print(f"  waiting for WSO2 ({attempt}/40)", flush=True)
        time.sleep(10)
    fail("WSO2 did not become ready within 400 seconds", "Check: docker logs urlshortener-wso2 --tail=100")


def register_client():
    return get_json(
        "POST",
        f"{WSO2_HOST}/client-registration/v0.17/register",
        headers={"Authorization": basic(WSO2_USER, WSO2_PASS)},
        json_body={
            "clientName": "urlshortener-seed-client",
            "owner": WSO2_USER,
            "grantType": "password refresh_token client_credentials",
            "saasApp": True,
        },
    )


def get_admin_token(client_id, client_secret):
    scope = (
        "apim:api_create apim:api_publish apim:api_view apim:subscribe "
        "apim:api_manage apim:app_manage apim:subscription_view "
        "apim:sub_manage apim:admin apim:tier_view apim:tier_manage "
        "apim:api_key"
    )
    return get_json(
        "POST",
        f"{WSO2_HOST}/oauth2/token",
        headers={"Authorization": basic(client_id, client_secret)},
        form={
            "grant_type": "password",
            "username": WSO2_USER,
            "password": WSO2_PASS,
            "scope": scope,
        },
    )


def publisher(method, path, body=None, allow_error=False):
    return request(
        method,
        f"{WSO2_HOST}/api/am/publisher/v4{path}",
        headers={"Authorization": f"Bearer {ADMIN_TOKEN}"},
        json_body=body,
        allow_error=allow_error,
    )


def admin_api(method, path, body=None, allow_error=False):
    return request(
        method,
        f"{WSO2_HOST}/api/am/admin/v4{path}",
        headers={"Authorization": f"Bearer {ADMIN_TOKEN}"},
        json_body=body,
        allow_error=allow_error,
    )


def devportal(method, path, body=None, allow_error=False):
    return request(
        method,
        f"{WSO2_HOST}/api/am/devportal/v3{path}",
        headers={"Authorization": f"Bearer {ADMIN_TOKEN}"},
        json_body=body,
        allow_error=allow_error,
    )


def mock_request(method, path, *, json_body=None, form=None, allow_error=False):
    if WINDOWS_CURL:
        url = f"{MOCK_ISSUER_URL}{path}"
        command = [WINDOWS_CURL, "-sS", "-X", method, url, "-w", "\n%{http_code}"]
        if json_body is not None:
            command.extend(["-H", "Content-Type: application/json", "-d", json.dumps(json_body)])
        if form is not None:
            command.extend(["-H", "Content-Type: application/x-www-form-urlencoded", "-d", urllib.parse.urlencode(form)])

        result = subprocess.run(command, capture_output=True, text=True)
        if result.returncode != 0:
            if allow_error:
                return 0, (result.stderr or result.stdout).strip()
            fail(f"{method} {url} failed", result.stderr or result.stdout)

        stdout = result.stdout.rstrip("\r\n")
        body, _, status_text = stdout.rpartition("\n")
        if not status_text.isdigit():
            body = stdout
            status_text = "200"
        status = int(status_text)
        if status >= 400 and not allow_error:
            fail(f"{method} {url} failed with HTTP {status}", body)
        return status, body

    return request(
        method,
        f"{MOCK_ISSUER_URL}{path}",
        json_body=json_body,
        form=form,
        allow_error=allow_error,
        context=None,
    )


def api_payload():
    return {
        "name": "URLShortener-API",
        "description": "URL Shortener Platform management API",
        "context": "/api/v1",
        "version": "1.0",
        "isDefaultVersion": True,
        "provider": "admin",
        "lifeCycleStatus": "CREATED",
        "type": "HTTP",
        "transport": ["http", "https"],
        "tags": ["urlshortener", "management"],
        "policies": ["Bronze", "Silver", "Gold"],
        "visibility": "PUBLIC",
        "securityScheme": ["oauth2", "api_key"],
        "endpointConfig": {
            "endpoint_type": "http",
            "production_endpoints": {
                "url": API_BACKEND_URL,
                "config": {"actionDuration": "30000", "actionSelect": "fault"},
            },
            "sandbox_endpoints": {"url": API_BACKEND_URL},
            "endpoint_security": {
                "production": {"enabled": False},
                "sandbox": {"enabled": False},
            },
        },
        "operations": [
            {"target": "/healthz", "verb": "GET", "authType": "None", "throttlingPolicy": "Unlimited", "scopes": []},
            {"target": "/urls", "verb": "POST", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces", "verb": "POST", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces", "verb": "GET", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["read"]},
            {"target": "/workspaces/{workspaceID}", "verb": "GET", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["read"]},
            {"target": "/workspaces/{workspaceID}/urls", "verb": "POST", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces/{workspaceID}/urls", "verb": "GET", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["read"]},
            {"target": "/workspaces/{workspaceID}/urls/{urlID}", "verb": "GET", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["read"]},
            {"target": "/workspaces/{workspaceID}/urls/{urlID}", "verb": "PATCH", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces/{workspaceID}/urls/{urlID}", "verb": "DELETE", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces/{workspaceID}/members", "verb": "GET", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["read"]},
            {"target": "/workspaces/{workspaceID}/members", "verb": "POST", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces/{workspaceID}/api-keys", "verb": "GET", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["read"]},
            {"target": "/workspaces/{workspaceID}/api-keys", "verb": "POST", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/workspaces/{workspaceID}/api-keys/{keyID}", "verb": "DELETE", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
            {"target": "/auth/token", "verb": "DELETE", "authType": "Application", "throttlingPolicy": "Unlimited", "scopes": ["write"]},
        ],
        "corsConfiguration": {
            "corsConfigurationEnabled": True,
            "accessControlAllowOrigins": ["*"],
            "accessControlAllowCredentials": False,
            "accessControlAllowHeaders": ["authorization", "Access-Control-Allow-Origin", "Content-Type", "SOAPAction", "apikey"],
            "accessControlAllowMethods": ["GET", "PUT", "POST", "DELETE", "PATCH", "OPTIONS"],
        },
    }


def redirect_payload():
    return {
        "name": "URLShortener-Redirect-API",
        "description": "URL Shortener public redirect service",
        "context": "/r",
        "version": "1.0",
        "isDefaultVersion": True,
        "provider": "admin",
        "lifeCycleStatus": "CREATED",
        "type": "HTTP",
        "transport": ["http", "https"],
        "tags": ["urlshortener", "redirect", "public"],
        "policies": ["Unlimited"],
        "visibility": "PUBLIC",
        "securityScheme": [],
        "endpointConfig": {
            "endpoint_type": "http",
            "production_endpoints": {
                "url": REDIRECT_BACKEND_URL,
                "config": {"actionDuration": "10000", "actionSelect": "fault"},
            },
            "sandbox_endpoints": {"url": REDIRECT_BACKEND_URL},
            "endpoint_security": {
                "production": {"enabled": False},
                "sandbox": {"enabled": False},
            },
        },
        "operations": [
            {"target": "/{shortcode}", "verb": "GET", "authType": "None", "throttlingPolicy": "Unlimited", "scopes": []}
        ],
        "corsConfiguration": {"corsConfigurationEnabled": False},
    }


def ensure_api(name, payload):
    query = urllib.parse.quote(f"name:{name} version:1.0")
    status, body = publisher("GET", f"/apis?query={query}")
    existing = (parse_json_response(status, body, "API lookup").get("list") or [{}])[0].get("id", "")

    if existing:
        log(f"Updating existing {name}: {existing}")
        status, body = publisher("GET", f"/apis/{existing}")
        current = parse_json_response(status, body, f"Read existing {name}")
        for key, value in payload.items():
            current[key] = value
        status, body = publisher("PUT", f"/apis/{existing}", current, allow_error=True)
        if status not in (200, 201):
            fail(f"Failed to update {name}", body)
        deploy_revision(existing, name)
        return existing

    status, body = publisher("POST", "/apis", payload, allow_error=True)
    created = parse_json_response(status, body, f"Create {name}")
    api_id = created.get("id")

    if not api_id:
        compat_payload = dict(payload)
        compat_payload["endpointConfig"] = json.dumps(payload["endpointConfig"])
        status, body = publisher("POST", "/apis", compat_payload, allow_error=True)
        created = parse_json_response(status, body, f"Create {name} compatibility retry")
        api_id = created.get("id")

    if not api_id:
        fail(f"Failed to create {name}", body)

    if created.get("lifeCycleStatus") != "PUBLISHED":
        publisher("POST", f"/apis/change-lifecycle?action=Publish&apiId={api_id}")
    deploy_revision(api_id, name)
    return api_id


def deploy_revision(api_id, name):
    status, body = create_revision(api_id, name)
    if status not in (200, 201):
        if status == 500 or "maximum" in body.lower():
            prune_stale_revisions(api_id)
            status, body = create_revision(api_id, name)
        if status not in (200, 201):
            fail(f"Failed to create revision for {name}", body)

    revision = parse_json_response(status, body, f"Create revision for {name}")
    revision_id = revision.get("id")
    if not revision_id:
        fail(f"Revision response for {name} did not include an id", body)

    status, body = publisher(
        "POST",
        f"/apis/{api_id}/deploy-revision?revisionId={urllib.parse.quote(revision_id)}",
        [{"name": "Default", "vhost": "localhost", "displayOnDevportal": True}],
        allow_error=True,
    )
    if status not in (200, 201):
        fail(f"Failed to deploy revision for {name}", body)


def create_revision(api_id, name):
    return publisher(
        "POST",
        f"/apis/{api_id}/revisions",
        {"description": f"Local deployment for {name}"},
        allow_error=True,
    )


def prune_stale_revisions(api_id):
    status, body = publisher("GET", f"/apis/{api_id}/revisions", allow_error=True)
    if status not in (200, 201):
        warn(f"Unable to list revisions for cleanup: HTTP {status} {body}")
        return
    revisions = parse_json_response(status, body, "List revisions").get("list") or []
    revisions.sort(key=lambda revision: revision.get("createdTime", 0))

    while len(revisions) >= 5:
        stale = next((revision for revision in revisions if not revision.get("deploymentInfo")), None)
        if stale is None:
            warn("All API revisions are deployed; skipping cleanup")
            return
        status, body = publisher("DELETE", f"/apis/{api_id}/revisions/{stale['id']}", allow_error=True)
        if status not in (200, 202, 204):
            warn(f"Failed to delete stale revision {stale['id']}: HTTP {status} {body}")
            return
        revisions = [revision for revision in revisions if revision.get("id") != stale["id"]]


def ensure_subscription_policy(name, request_count, description):
    status, body = admin_api("GET", "/throttling/policies/subscription")
    policies = parse_json_response(status, body, "List subscription policies").get("list") or []
    desired_limit = {
        "type": "REQUESTCOUNTLIMIT",
        "requestCount": {
            "timeUnit": "min",
            "unitTime": 1,
            "requestCount": request_count,
        },
    }
    existing = next((policy for policy in policies if policy.get("policyName") == name), None)
    if existing is None:
        status, body = admin_api(
            "POST",
            "/throttling/policies/subscription",
            {
                "policyName": name,
                "displayName": name,
                "description": description,
                "defaultLimit": desired_limit,
                "stopOnQuotaReach": True,
                "billingPlan": "FREE",
                "customAttributes": [],
            },
            allow_error=True,
        )
        if status not in (200, 201):
            fail(f"Failed to create subscription policy {name}", body)
        return

    limit = (((existing.get("defaultLimit") or {}).get("requestCount")) or {})
    if (
        existing.get("description") == description
        and limit.get("timeUnit") == "min"
        and limit.get("unitTime") == 1
        and limit.get("requestCount") == request_count
    ):
        return

    existing["displayName"] = name
    existing["description"] = description
    existing["defaultLimit"] = desired_limit
    existing["stopOnQuotaReach"] = True
    existing["billingPlan"] = "FREE"
    existing["customAttributes"] = existing.get("customAttributes") or []
    status, body = admin_api(
        "PUT",
        f"/throttling/policies/subscription/{existing['policyId']}",
        existing,
        allow_error=True,
    )
    if status not in (200, 201):
        fail(f"Failed to update subscription policy {name}", body)


def application_by_name(name):
    status, body = devportal("GET", f"/applications?query={urllib.parse.quote(name)}", allow_error=True)
    if status not in (200, 201):
        return {}
    apps = parse_json_response(status, body, "Application lookup").get("list") or []
    return apps[0] if apps else {}


def delete_application(app_id):
    status, body = devportal("DELETE", f"/applications/{app_id}", allow_error=True)
    if status not in (200, 202, 204, 404):
        warn(f"Application delete returned HTTP {status}: {body}")


def ensure_app():
    existing = application_by_name("URLShortener-DevApp")
    app_id = existing.get("applicationId", "")
    if app_id:
        status, body = devportal("GET", f"/applications/{app_id}", allow_error=True)
        if status in (200, 201):
            current = parse_json_response(status, body, "Read application")
            if current.get("tokenType") != "OAUTH":
                warn(f"Recreating URLShortener-DevApp because tokenType={current.get('tokenType')} is not compatible with external key mapping")
                delete_application(app_id)
                app_id = ""
            elif RESET:
                warn(f"Deleting URLShortener-DevApp for reset: {app_id}")
                delete_application(app_id)
                app_id = ""

    if app_id:
        warn(f"URLShortener-DevApp already exists; reusing {app_id}")
        return app_id

    status, body = devportal(
        "POST",
        "/applications",
        {
            "name": "URLShortener-DevApp",
            "throttlingPolicy": "Unlimited",
            "description": "Default local developer app for URL Shortener APIs",
            "tokenType": "OAUTH",
        },
        allow_error=True,
    )
    if status not in (200, 201):
        fail("Failed to create developer application", body)
    app_id = parse_json_response(status, body, "Create application").get("applicationId")
    if not app_id:
        fail("Developer application response did not include applicationId", body)
    return app_id


def subscribe(app_id, api_id, policy):
    status, body = devportal("GET", f"/subscriptions?applicationId={app_id}", allow_error=True)
    existing_subs = []
    if status in (200, 201):
        existing_subs = parse_json_response(status, body, "List subscriptions").get("list") or []
    current = next((sub for sub in existing_subs if sub.get("apiId") == api_id), None)
    if current and current.get("throttlingPolicy") == policy:
        return
    if current:
        status, body = devportal("DELETE", f"/subscriptions/{current['subscriptionId']}", allow_error=True)
        if status not in (200, 202, 204, 404):
            warn(f"Subscription delete returned HTTP {status}: {body}")

    status, body = devportal(
        "POST",
        "/subscriptions",
        {"applicationId": app_id, "apiId": api_id, "throttlingPolicy": policy},
        allow_error=True,
    )
    if status in (200, 201, 409):
        return
    if "already" in body.lower() or "duplicate" in body.lower():
        return
    warn(f"Subscription may not have been created: HTTP {status} {body}")


def generate_keys(app_id):
    status, body = devportal(
        "POST",
        f"/applications/{app_id}/generate-keys",
        {
            "keyType": "PRODUCTION",
            "grantTypesToBeSupported": ["client_credentials", "password", "refresh_token"],
            "validityTime": 3600,
            "scopes": ["read", "write"],
            "additionalProperties": {},
        },
        allow_error=True,
    )
    if status not in (200, 201):
        warn(f"Key generation returned HTTP {status}: {body}")
        return {}
    return parse_json_response(status, body, "Generate keys")


def generate_api_key(app_id):
    status, body = devportal(
        "POST",
        f"/applications/{app_id}/api-keys/PRODUCTION/generate",
        {"validityPeriod": 3600},
        allow_error=True,
    )
    if status not in (200, 201):
        warn(f"API key generation returned HTTP {status}: {body}")
        return {}
    return parse_json_response(status, body, "Generate API key")


def mock_issuer_ready():
    status, body = mock_request("GET", "/healthz", allow_error=True)
    return status == 200 and "alive" in body


def mock_key_manager_payload(existing_id=""):
    payload = {
        "name": MOCK_KEY_MANAGER_NAME,
        "displayName": "URLShortener Mock Issuer",
        "type": "WSO2-IS",
        "description": "Local mock issuer for direct JWT validation",
        "wellKnownEndpoint": "http://host.docker.internal:9000/.well-known/openid-configuration",
        "introspectionEndpoint": "http://host.docker.internal:9000/oauth2/introspect",
        "clientRegistrationEndpoint": "http://host.docker.internal:9000/oauth2/dcr/register",
        "tokenEndpoint": "http://host.docker.internal:9000/token",
        "displayTokenEndpoint": f"{MOCK_ISSUER_PUBLIC_URL}/token",
        "revokeEndpoint": "http://host.docker.internal:9000/oauth2/revoke",
        "displayRevokeEndpoint": f"{MOCK_ISSUER_PUBLIC_URL}/oauth2/revoke",
        "userInfoEndpoint": None,
        "authorizeEndpoint": None,
        "endpoints": [],
        "certificates": {
            "type": "JWKS",
            "value": "http://host.docker.internal:9000/.well-known/jwks.json",
        },
        "issuer": MOCK_ISSUER_PUBLIC_URL,
        "alias": None,
        "scopeManagementEndpoint": None,
        "availableGrantTypes": ["client_credentials"],
        "enableTokenGeneration": True,
        "enableTokenEncryption": False,
        "enableTokenHashing": False,
        "enableMapOAuthConsumerApps": True,
        "enableOAuthAppCreation": True,
        "enableSelfValidationJWT": True,
        "claimMapping": [
            {"remoteClaim": "workspace_id", "localClaim": "http://wso2.org/claims/workspace_id"},
            {"remoteClaim": "scope", "localClaim": "http://wso2.org/claims/scope"},
        ],
        "consumerKeyClaim": "client_id",
        "scopesClaim": "scope",
        "tokenValidation": [{"id": None, "enable": True, "type": "JWT", "value": ""}],
        "enabled": True,
        "global": False,
        "additionalProperties": {
            "validation_enable": True,
            "self_validate_jwt": True,
            "km_admin_as_app_owner": False,
            "TokenURL": "http://host.docker.internal:9000/token",
            "Password": "admin",
            "ServerURL": "http://host.docker.internal:9000/services/",
            "Username": "admin",
            "RevokeURL": "http://host.docker.internal:9000/oauth2/revoke",
        },
        "permissions": {"permissionType": "PUBLIC", "roles": []},
        "tokenType": "DIRECT",
    }
    if existing_id:
        payload["id"] = existing_id
    return payload


def ensure_mock_key_manager():
    if not mock_issuer_ready():
        warn("Mock issuer is not running; skipping external JWT key manager setup")
        return {}

    status, body = admin_api("GET", "/key-managers", allow_error=True)
    if status not in (200, 201):
        warn(f"Failed to list key managers: HTTP {status} {body}")
        return {}
    managers = parse_json_response(status, body, "List key managers").get("list") or []
    current = next((item for item in managers if item.get("name") == MOCK_KEY_MANAGER_NAME), None)

    if current:
        key_manager_id = current.get("id")
        status, body = admin_api("PUT", f"/key-managers/{key_manager_id}", mock_key_manager_payload(key_manager_id), allow_error=True)
        if status not in (200, 201):
            warn(f"Failed to update mock issuer key manager: HTTP {status} {body}")
            return {}
        return parse_json_response(status, body, "Update key manager")

    status, body = admin_api("POST", "/key-managers", mock_key_manager_payload(), allow_error=True)
    if status not in (200, 201):
        warn(f"Failed to create mock issuer key manager: HTTP {status} {body}")
        return {}
    return parse_json_response(status, body, "Create key manager")


def create_mock_client(app_id):
    status, body = mock_request(
        "POST",
        "/oauth2/dcr/register",
        json_body={"clientName": f"URLShortener-DevApp-{app_id}", "grantType": ["client_credentials"]},
        allow_error=True,
    )
    if status not in (200, 201):
        warn(f"Mock issuer DCR failed: HTTP {status} {body}")
        return {}
    return parse_json_response(status, body, "Create mock OAuth client")


def map_mock_keys(app_id, client_id, client_secret):
    status, body = devportal(
        "POST",
        f"/applications/{app_id}/map-keys",
        {
            "consumerKey": client_id,
            "consumerSecret": client_secret,
            "keyManager": MOCK_KEY_MANAGER_NAME,
            "keyType": "PRODUCTION",
        },
        allow_error=True,
    )
    if status not in (200, 201):
        warn(f"Mapping external mock keys failed: HTTP {status} {body}")
        return {}
    return parse_json_response(status, body, "Map external keys")


def provision_mock_keys(app_id):
    if not mock_issuer_ready():
        return {}

    key_manager = ensure_mock_key_manager()
    if not key_manager:
        return {}

    client = create_mock_client(app_id)
    client_id = client.get("clientId", "")
    client_secret = client.get("clientSecret", "")
    if not client_id or not client_secret:
        warn(f"Mock issuer did not return usable client credentials: {json.dumps(client)}")
        return {}

    mapped = map_mock_keys(app_id, client_id, client_secret)
    if not mapped:
        return {}

    return {
        "keyManager": key_manager.get("name", MOCK_KEY_MANAGER_NAME),
        "consumerKey": client_id,
        "consumerSecret": client_secret,
    }


log("Waiting for WSO2 API Manager")
wait_for_wso2()

log("Registering seed OAuth client")
dcr = register_client()
client_id = dcr.get("clientId")
client_secret = dcr.get("clientSecret")
if not client_id or not client_secret:
    fail("Failed to register seed OAuth client", json.dumps(dcr))

log("Obtaining admin token")
token_response = get_admin_token(client_id, client_secret)
ADMIN_TOKEN = token_response.get("access_token")
if not ADMIN_TOKEN:
    fail("Failed to obtain admin token", json.dumps(token_response))

log("Ensuring subscription throttling policies")
ensure_subscription_policy("Bronze", 1000, "Allows 1000 requests per minute")
ensure_subscription_policy("Silver", 5000, "Allows 5000 requests per minute")
ensure_subscription_policy("Gold", 20000, "Allows 20000 requests per minute")

log("Creating or updating APIs")
api_id = ensure_api("URLShortener-API", api_payload())
redirect_api_id = ensure_api("URLShortener-Redirect-API", redirect_payload())

log("Creating developer application and subscriptions")
app_id = ensure_app()
subscribe(app_id, api_id, "Bronze")
subscribe(app_id, redirect_api_id, "Unlimited")

log("Generating Resident Key Manager credentials")
keys = generate_keys(app_id)
consumer_key = keys.get("consumerKey", "")
consumer_secret = keys.get("consumerSecret", "")
api_key = generate_api_key(app_id).get("apikey", "")

mock_keys = {}
if mock_issuer_ready():
    log("Provisioning external mock issuer credentials")
    mock_keys = provision_mock_keys(app_id)
else:
    warn("Mock issuer is not reachable; skipping external JWT setup")

print("\nWSO2 API Manager seeding complete.\n")
print("APIs:")
print(f"  URLShortener-API           {api_id}")
print(f"  URLShortener-Redirect-API  {redirect_api_id}")
print("\nApplication:")
print(f"  URLShortener-DevApp        {app_id}")
print("\nGateway routes:")
print("  Management API: http://localhost:8280/api/v1/1.0")
print("  Redirect API:   http://localhost:8280/r/1.0")
print("\nPortals:")
print("  Publisher:      https://localhost:9443/publisher")
print("  Developer:      https://localhost:9443/devportal")
print("  Credentials:    admin/admin")

if consumer_key:
    print("\nResident Key Manager OAuth2 credentials:")
    print(f"  Consumer Key:    {consumer_key}")
    print(f"  Consumer Secret: {consumer_secret}")
    print("\nResident token command:")
    print(
        "  curl -sk -X POST https://localhost:9443/oauth2/token "
        f"-u '{consumer_key}:{consumer_secret}' "
        "-d 'grant_type=client_credentials&scope=read write'"
    )
else:
    warn("WSO2 did not return new resident OAuth keys. Use --reset to recreate the dev app if you need fresh credentials.")

if api_key:
    print("\nResident Key Manager API key:")
    print(f"  apikey: {api_key}")

if mock_keys:
    print("\nMock issuer client credentials:")
    print(f"  Key Manager:      {mock_keys['keyManager']}")
    print(f"  Consumer Key:     {mock_keys['consumerKey']}")
    print(f"  Consumer Secret:  {mock_keys['consumerSecret']}")
    print("\nMock issuer token command:")
    print(
        "  curl -s -X POST "
        f"{MOCK_ISSUER_URL}/token "
        f"-d 'grant_type=client_credentials&client_id={mock_keys['consumerKey']}&client_secret={mock_keys['consumerSecret']}&workspace_id=ws_default&user_id=usr_001&scope=read write'"
    )
else:
    warn("Mock issuer client credentials were not provisioned. Start the mock issuer and run this script with --reset.")
PY
