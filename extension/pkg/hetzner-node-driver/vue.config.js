const path = require('path');

const baseConfig = require('./.shell/pkg/vue.config')(path.resolve(__dirname));

const origConfigureWebpack = baseConfig.configureWebpack;

baseConfig.configureWebpack = (config) => {
  if (typeof origConfigureWebpack === 'function') {
    origConfigureWebpack(config);
  }

  if (!config.resolve.extensions.includes('.ts')) {
    config.resolve.extensions.unshift('.ts', '.tsx');
  }

  config.module.rules.push({
    test: /\.tsx?$/,
    use:  [
      {
        loader:  'ts-loader',
        options: {
          transpileOnly:         true,
          appendTsSuffixTo:      [/\.vue$/],
          happyPackMode:         false,
          configFile:            path.resolve(__dirname, 'tsconfig.json'),
        },
      },
    ],
  });
};

module.exports = baseConfig;
