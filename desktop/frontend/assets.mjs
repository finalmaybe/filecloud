import { cp, mkdir } from "node:fs/promises";
const dist = new URL("./dist/", import.meta.url);
await mkdir(dist, { recursive: true });
await cp(new URL("./public/", import.meta.url), dist, { recursive: true });
for (const name of ["app-icon-1024.png", "filecloud-logo.svg"]) {
  await cp(
    new URL(`../../assets/brand/${name}`, import.meta.url),
    new URL(name, dist),
  );
}
