import { cp, mkdir } from "node:fs/promises";
const dist = new URL("./dist/", import.meta.url);
await mkdir(dist, { recursive: true });
await cp(new URL("./public/", import.meta.url), dist, { recursive: true });
const pdf = new URL("./node_modules/pdfjs-dist/", import.meta.url);
await mkdir(new URL("pdfjs/", dist), { recursive: true });
for (const name of ["pdf.min.mjs", "pdf.worker.min.mjs"]) {
  await cp(
    new URL(`legacy/build/${name}`, pdf),
    new URL(`pdfjs/${name}`, dist),
  );
}
for (const name of ["cmaps", "standard_fonts", "LICENSE"]) {
  await cp(new URL(name, pdf), new URL(`pdfjs/${name}`, dist), {
    recursive: true,
  });
}
await cp(
  new URL("../build/icons/app-icon-1024.png", import.meta.url),
  new URL("app-icon-1024.png", dist),
);
