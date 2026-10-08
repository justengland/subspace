# OpenAPI → TypeScript client

Contract: `openapi/openapi.yaml` (mirrors `backend/api` HTTP routes).

Regenerate the frontend client after API changes:

```bash
cd frontend && npm run codegen
```

Runs `openapi-typescript` against this YAML into `frontend/src/api/schema.d.ts`. Commit both.
