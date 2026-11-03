// ESLint's built-in rules only, so the fixture needs no packages.
export default [
  {
    files: ["src/**/*.js"],
    languageOptions: { ecmaVersion: 2022, sourceType: "module", globals: { console: "readonly" } },
    rules: {
      "no-unused-vars": "error",
      "no-undef": "error",
      "eqeqeq": "error",
      "no-console": "warn",
      "prefer-const": "warn",
      "no-var": "warn",
      "no-dupe-keys": "error",
      "no-unreachable": "error",
    },
  },
];
