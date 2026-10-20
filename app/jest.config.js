module.exports = {
  preset: 'react-native',
  setupFiles: ['<rootDir>/jest.setup.js'],
  // lucide-react-native's package "exports" points Jest at its ESM build, which
  // the react-native preset doesn't transform; use its CommonJS build instead.
  moduleNameMapper: {
    '^lucide-react-native$': '<rootDir>/node_modules/lucide-react-native/dist/cjs/lucide-react-native.js',
  },
};
