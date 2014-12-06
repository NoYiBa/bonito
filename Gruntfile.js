module.exports = function(grunt) {

  // Project configuration
  grunt.initConfig({
    pkg: grunt.file.readJSON('package.json'),
    jshint: {
      all: [
        'Gruntfile.js',
        'src/**/*.js'
      ]
    }
  });

  // Plugins
  grunt.loadNpmTasks('grunt-contrib-jshint');

  // Default task(s)
  grunt.registerTask('default', ['jshint']);

};
