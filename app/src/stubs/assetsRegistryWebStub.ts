// Stand-in for @react-native/assets-registry/registry on the web build.
// react-native-svg's asset-URI resolver (used for embedding local raster
// images via <Image>/xlink refs inside an SVG) imports this unconditionally
// — including from its own web-targeted build — but the real package ships
// raw Flow syntax (`export type PackagerAsset = {...}`) that Rollup can't
// parse, so Vite's production build fails the moment react-native-svg is
// reachable at all, even though we never embed raster images in an SVG
// (DataPointChart only draws lines/circles). Aliased in (see vite.config.ts)
// purely so Vite never has to parse the real package.
export function getAssetByID(): undefined {
  return undefined;
}

export function registerAsset(asset: unknown): unknown {
  return asset;
}
