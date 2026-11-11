import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: { "@hedgehood/maker": fileURLToPath(new URL("./src/index.ts", import.meta.url)) },
  },
  test: {
    include: ["test/**/*.test.ts"],
    testTimeout: 20_000,
  },
});
