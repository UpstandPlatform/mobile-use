# mobile-use product landing

Standalone Next.js landing app for the mobile-use project. It uses the shadcn/ui component workflow with Base UI primitives, Tailwind CSS 4, and Motion for scroll-linked storytelling.

## Run locally

```sh
npm ci
npm run dev
```

Open `http://localhost:3000`.

## Deploy on Vercel

Import the `UpstandPlatform/mobile-use` repository into Vercel and set the project **Root Directory** to the repository root (`.`), where the root `vercel.json` is located. The `landing` service builds this folder with the Next.js framework, and the project's public catch-all route points only to that service. The app has no required environment variables; the download, source, and setup links point to the public GitHub project and its latest release.

For local Vercel routing, run `npx vercel dev --local` from the repository root. To work on the landing app alone, run the npm commands below from this folder.

For a local production check:

```sh
npm ci
npm run lint
npm run typecheck
npm run build
```

Product setup and platform requirements live in the [main quickstart](../../README.md), [deployment guide](../../docs/operations.md), and [verification status](../../docs/verification.md).
