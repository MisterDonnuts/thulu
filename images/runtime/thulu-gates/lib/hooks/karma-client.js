// Loaded in the browser by thulu-gates' Karma config: tells the server each spec as it starts, so
// a spec that never ends can be named. Jasmine reports specStarted; Mocha and others are left alone.
(function () {
  var karma = window.__karma__;
  var jasmineEnv = window.jasmine && window.jasmine.getEnv && window.jasmine.getEnv();
  if (!karma || !jasmineEnv || !jasmineEnv.addReporter) return;
  jasmineEnv.addReporter({
    specStarted: function (r) {
      karma.info({ thuluGates: { name: r.fullName } });
    },
  });
})();
