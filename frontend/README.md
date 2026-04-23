# USP Control Plane

Production-grade React dashboard for the cloud-native URL Shortener platform.

## Stack

- React 18 + TypeScript
- Vite
- Tailwind CSS
- TanStack Query
- Axios
- Recharts
- Zustand

## Local Development

```bash
cd frontend
cp .env.example .env
npm install
npm run dev
```

The app expects the backend API at `VITE_API_URL`, defaulting to `http://localhost:8080`.

## Required Runtime Inputs

- `JWT token`: paste into the session form in the dashboard
- `workspace ID`: the workspace ULID or identifier encoded in your token
- `user ID`: optional, used for UI context

## Build

```bash
cd frontend
npm run build
npm run preview
```

## Docker

```bash
cd frontend
docker build -t usp-control-plane .
docker run --rm -p 3000:80 usp-control-plane
```

## Deploy To Vercel

1. Import the `frontend` directory as a Vercel project.
2. Set `VITE_API_URL` in Project Settings.
3. Use the default build command `npm run build`.
4. Set the output directory to `dist`.

If the backend is hosted on a different origin, ensure CORS allows the Vercel domain.

## Backend Contract Notes

- URL creation uses `POST /api/v1/workspaces/{workspaceID}/urls`.
- Analytics in the current Go backend are URL-scoped, so the dashboard lets operators select a URL and inspect its 24h, 7d, and 30d views.
- Workspace live stream uses `GET /api/v1/workspaces/{workspaceID}/stream`.
- The SSE client uses an EventSource-compatible polyfill so Bearer auth headers can be sent to the backend.
