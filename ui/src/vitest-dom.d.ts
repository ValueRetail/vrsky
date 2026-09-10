/**
 * Brings the jest-dom matcher types into the app's TypeScript program.
 *
 * tests/setup.ts imports '@testing-library/jest-dom/vitest' at runtime, but
 * tsconfig.app.json has `include: ["src"]`, so that file is never part of the
 * program and its `declare module 'vitest'` augmentation never applies. Tests
 * then run green while `tsc -b` — which CI runs via `npm run build` — fails on
 * every toBeInTheDocument/toBeEnabled/toHaveTextContent.
 *
 * It went unnoticed because the only tests here before now were pure logic and
 * never touched the DOM.
 */
import '@testing-library/jest-dom/vitest'
