import React from 'react';
import {StyleSheet} from 'react-native';

// Stand-in for react-native/Libraries/Utilities/codegenNativeComponent on
// the web build. react-native-svg's element components (Circle, Path, Svg,
// ...) each import this — via a Fabric native-component file, e.g.
// fabric/CircleNativeComponent.js does
//   codegenNativeComponent('RNSVGCircle')
// — to get their leaf renderer. There is no react-native-web equivalent:
// RNW never implements native-view registration (no requireNativeComponent,
// no TurboModuleRegistry), so the generic "react-native" -> "react-native-web"
// alias can't cover this one (see vite.config.ts).
//
// Rather than stub it out to a no-op (which would silently drop all shapes),
// map each RNSVG native-component name straight to the matching real DOM SVG
// tag. react-native-svg already computes SVG-correct prop values upstream in
// its extract*.ts helpers before handing them to this leaf component, and
// those prop names (fill, stroke, strokeWidth, cx, cy, r, d, ...) match React
// DOM's SVG attribute casing closely enough to pass straight through. This
// only needs to cover the tags DataPointChart.tsx actually renders
// (Svg/Path/Line/Circle, plus the <G> wrapper Svg always renders internally)
// — other RNSVG elements fall back to <g> so an unsupported shape degrades
// instead of crashing the whole tree.
const TAG_BY_NAME: Record<string, string> = {
  RNSVGSvgView: 'svg',
  RNSVGSvgViewAndroid: 'svg',
  RNSVGGroup: 'g',
  RNSVGPath: 'path',
  RNSVGLine: 'line',
  RNSVGCircle: 'circle',
  RNSVGRect: 'rect',
  RNSVGEllipse: 'ellipse',
  RNSVGDefs: 'defs',
  RNSVGUse: 'use',
  RNSVGSymbol: 'symbol',
  RNSVGText: 'text',
  RNSVGTSpan: 'tspan',
  RNSVGTextPath: 'textPath',
  RNSVGClipPath: 'clipPath',
  RNSVGMask: 'mask',
  RNSVGPattern: 'pattern',
  RNSVGMarker: 'marker',
  RNSVGImage: 'image',
  RNSVGForeignObject: 'foreignObject',
  RNSVGLinearGradient: 'linearGradient',
  RNSVGRadialGradient: 'radialGradient',
  RNSVGFilter: 'filter',
  RNSVGFeBlend: 'feBlend',
  RNSVGFeColorMatrix: 'feColorMatrix',
  RNSVGFeFlood: 'feFlood',
  RNSVGFeGaussianBlur: 'feGaussianBlur',
  RNSVGFeMerge: 'feMerge',
  RNSVGFeOffset: 'feOffset',
};

// Props react-native-svg's element classes pass down that are meaningful to
// RN's native-view bridge but not valid (or not spelled the same) as DOM SVG
// attributes — forwarding them as-is makes React warn or throw.
const DROP_PROPS = new Set([
  'bbWidth',
  'bbHeight',
  'tintColor',
  'focusable',
  'responsible',
  'onStartShouldSetResponder',
  'onResponderGrant',
  'onResponderMove',
  'onResponderRelease',
  'onResponderTerminate',
  'onResponderTerminationRequest',
  'onLayout',
]);

export default function codegenNativeComponent(name: string): React.ComponentType<any> {
  const tag = TAG_BY_NAME[name] ?? 'g';
  return React.forwardRef(function RNSVGWebComponent(props: any, ref) {
    const {style, ...rest} = props;
    const domProps: Record<string, unknown> = {};
    for (const key of Object.keys(rest)) {
      if (!DROP_PROPS.has(key)) domProps[key] = rest[key];
    }
    if (style) {
      domProps.style = StyleSheet.flatten(style);
    }
    return React.createElement(tag, {...domProps, ref});
  });
}
