/* eslint-disable no-undef */
module.exports = {
  content: [
    './app/index.html',
    './app/templates/**/*.hbs',
    './app/components/**/*.hbs',
    './app/components/**/*.js',
    './node_modules/mx-ui-components/addon/components/**/*.js',
    './node_modules/mx-ui-components/addon/components/**/*.hbs',
  ],
  safelist: [
    {
      pattern: /mxa-btn\S*/,
    },
  ],
  theme: {
    extend: {
      fontSize: {
        tiny: '0.625rem',
        double: '2rem',
        big: '1.125rem',
      },
      colors: {
        'transparent-teal': 'rgba(0, 161, 145, 0.1)',
      },
      spacing: {
        26: '6.5rem',
        76: '20.5rem',
        152: '41rem',
        228: '61.5rem',
      },
      animation: {
        spinrev: 'spin 1s linear infinite reverse',
      },
    },
  },
  plugins: [require('@meroxa/ui-base')],
};
